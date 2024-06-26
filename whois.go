/*
 * Copyright 2014-2023 Li Kexian
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Go module for domain and ip whois information query
 * https://www.likexian.com/
 */

package whois

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

var (
	serverMapInstance *serverMap
	onceWhois         sync.Once
)

const (
	// defaultWhoisServer is iana whois server
	defaultWhoisServer = "whois.iana.org"
	// defaultWhoisPort is default whois port
	defaultWhoisPort = "43"
	// defaultElapsedTimeout
	defaultElapsedTimeout = 15 * time.Second
	// defaultTimeout is query default timeout
	defaultTimeout = 5 * time.Second
)

// DefaultClient is default whois client
var DefaultClient = NewClient()

// Client is whois client
type Client struct {
	dialer          proxy.Dialer
	timeout         time.Duration
	elapsed         time.Duration
	disableStats    bool
	disableReferral bool

	serverMap *serverMap
}

// Version returns package version
func Version() string {
	return "1.15.0"
}

// Author returns package author
func Author() string {
	return "[Li Kexian](https://www.likexian.com/)"
}

// License returns package license
func License() string {
	return "Licensed under the Apache License 2.0"
}

// Whois do the whois query and returns whois information
func Whois(domain string, servers ...string) (result string, err error) {
	return DefaultClient.Whois(domain, servers...)
}

// NewClient returns new whois client
func NewClient() *Client {
	return &Client{
		dialer: &net.Dialer{
			Timeout: defaultTimeout,
		},
		timeout:   defaultElapsedTimeout,
		serverMap: serverMapInstance,
	}
}

// SetDialer set query net dialer
func (c *Client) SetDialer(dialer proxy.Dialer) *Client {
	c.dialer = dialer
	return c
}

// SetTimeout set query timeout
func (c *Client) SetTimeout(timeout time.Duration) *Client {
	c.timeout = timeout
	return c
}

// SetDisableStats set disable stats
func (c *Client) SetDisableStats(disabled bool) *Client {
	c.disableStats = disabled
	return c
}

// SetDisableReferral if set to true, will not query the referral server.
func (c *Client) SetDisableReferral(disabled bool) *Client {
	c.disableReferral = disabled
	return c
}

// Whois do the whois query and returns whois information
func (c *Client) Whois(domain string, servers ...string) (result string, err error) {
	start := time.Now()
	defer func() {
		result = strings.TrimSpace(result)
		if result != "" && !c.disableStats {
			result = fmt.Sprintf("%s\n\n%% Query time: %d msec\n%% WHEN: %s\n",
				result, time.Since(start).Milliseconds(), start.Format("Mon Jan 02 15:04:05 MST 2006"),
			)
		}
	}()

	domain = strings.Trim(strings.TrimSpace(domain), ".")
	if domain == "" {
		return "", ErrDomainEmpty
	}

	isASN := IsASN(domain)
	if isASN {
		if !strings.HasPrefix(strings.ToUpper(domain), asnPrefix) {
			domain = asnPrefix + domain
		}
	}

	if !strings.Contains(domain, ".") && !strings.Contains(domain, ":") && !isASN {
		return c.rawQuery(domain, defaultWhoisServer, defaultWhoisPort)
	}

	var server, port string
	if len(servers) > 0 && servers[0] != "" {
		server = strings.ToLower(servers[0])
		port = defaultWhoisPort
	} else {
		ext := getExtension(domain)
		if v, ok := c.serverMap.GetWhoisServer(ext); ok {
			// 如果tld存在于map中，更新server变量为map中对应的值
			server = v
			port = defaultWhoisPort
		} else {
			result, err := c.rawQuery(ext, defaultWhoisServer, defaultWhoisPort)
			if err != nil {
				return "", fmt.Errorf("whois: query for whois server failed: %w", err)
			}
			server, port = getServer(result)
			if server == "" {
				return "", fmt.Errorf("%w: %s", ErrWhoisServerNotFound, domain)
			}
			// 将最新查询到的tld服务器存到map中
			c.serverMap.SetWhoisServer(ext, server)
		}
	}

	result, err = c.rawQuery(domain, server, port)
	if err != nil {
		return
	}

	if c.disableReferral {
		return
	}

	refServer, refPort := getServer(result)
	if refServer == "" || refServer == server {
		return
	}

	data, err := c.rawQuery(domain, refServer, refPort)
	if err == nil {
		result += data
	}

	return
}

// rawQuery do raw query to the server
func (c *Client) rawQuery(domain, server, port string) (string, error) {
	c.elapsed = 0
	// start := time.Now()
	if server == "whois.arin.net" {
		if IsASN(domain) {
			domain = "a + " + domain
		} else {
			domain = "n + " + domain
		}
	}

	if value, ok := c.serverMap.GetRewriteServer(server); ok {
		// 如果键存在于map中，更新server变量为map中对应的值
		server = value
	}

	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()

	// conn, err := c.dialer.DialContext(ctx, "tcp", net.JoinHostPort(server, port))
	conn, err := dialContext(ctx, c.dialer, "tcp", net.JoinHostPort(server, port))
	if err != nil {
		return "", fmt.Errorf("whois: connect to whois server (%s) failed: %w", server, err)
	}

	defer conn.Close()
	// c.elapsed = time.Since(start)

	// _ = conn.SetWriteDeadline(time.Now().Add(c.timeout - c.elapsed))
	_, err = conn.Write([]byte(domain + "\r\n"))
	if err != nil {
		return "", fmt.Errorf("whois: send to whois server (%s) failed: %w", server, err)
	}

	// c.elapsed = time.Since(start)

	// _ = conn.SetReadDeadline(time.Now().Add(c.timeout - c.elapsed))
	buffer, err := io.ReadAll(conn)
	if err != nil {
		return "", fmt.Errorf("whois: read from whois server (%s) failed: %w", server, err)
	}

	// c.elapsed = time.Since(start)

	return string(buffer), nil
}

// getServer returns server from whois data
func getServer(data string) (string, string) {
	tokens := []string{
		"Registrar WHOIS Server: ",
		"whois: ",
		"ReferralServer: ",
		"refer: ",
	}

	for _, token := range tokens {
		start := strings.Index(data, token)
		if start != -1 {
			start += len(token)
			end := strings.Index(data[start:], "\n")
			if end == -1 { // 如果没有找到换行符，使用整个字符串
				end = len(data[start:])
			}
			server := strings.TrimSpace(data[start : start+end])

			// 新增代码：从URL提取主机名
			server = extractHostname(server)

			port := defaultWhoisPort
			if strings.Contains(server, ":") {
				v := strings.Split(server, ":")
				server, port = v[0], v[1]
			}
			return server, port
		}
	}

	return "", ""
}

// dialContext 尝试使用给定的代理Dialer和context来建立连接
func dialContext(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	// 注意：这里仅为示例，实际上golang.org/x/net/proxy包的Dialer可能不直接支持context。
	// 如果你的代理Dialer支持DialContext，直接使用它。
	// 否则，你需要根据具体的Dialer实现调整此函数。
	ch := make(chan net.Conn, 1)
	var dialErr error
	go func() {
		conn, err := dialer.Dial(network, addr)
		if err != nil {
			dialErr = err
			ch <- nil
			return
		}
		ch <- conn
	}()

	select {
	case conn := <-ch:
		return conn, dialErr
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// sync.Once的作用是确保在多线程环境下一个操作只被执行一次
func InitWhois(configFile string) {
	onceWhois.Do(func() {
		serverMapInstance = NewServerMap()
		err := serverMapInstance.LoadFromFile(configFile)
		if err != nil {
			// Handle error, e.g., log it, panic, etc.
			panic(err)
		}
	})
}
