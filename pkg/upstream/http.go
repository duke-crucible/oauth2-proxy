package upstream

import (
	"crypto/tls"
        "net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
        "time"
	"github.com/mbland/hmacauth"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/middleware"
	"github.com/oauth2-proxy/oauth2-proxy/v7/pkg/apis/options"
)

const (
	// SignatureHeader is the name of the request header containing the GAP Signature
	// Part of hmacauth
	SignatureHeader = "GAP-Signature"

	httpScheme  = "http"
	httpsScheme = "https"
)

// SignatureHeaders contains the headers to be signed by the hmac algorithm
// Part of hmacauth
var SignatureHeaders = []string{
	"Content-Length",
	"Content-Md5",
	"Content-Type",
	"Date",
	"Authorization",
	"X-Forwarded-User",
	"X-Forwarded-Email",
	"X-Forwarded-Preferred-User",
	"X-Forwarded-Access-Token",
	"Cookie",
	"Gap-Auth",
}

// newHTTPUpstreamProxy creates a new httpUpstreamProxy that can serve requests
// to a single upstream host.
func newHTTPUpstreamProxy(upstream options.Upstream, u *url.URL, sigData *options.SignatureData, errorHandler ProxyErrorHandler) http.Handler {
	// Set path to empty so that request paths start at the server root
	u.Path = ""

	// Create a ReverseProxy
	proxy := newReverseProxy(u, upstream, errorHandler)

	// Set up a WebSocket proxy if required
	var wsProxy http.Handler
	if upstream.ProxyWebSockets == nil || *upstream.ProxyWebSockets {
		wsProxy = newWebSocketReverseProxy(u, upstream.InsecureSkipTLSVerify)
	}

	var auth hmacauth.HmacAuth
	if sigData != nil {
		auth = hmacauth.NewHmacAuth(sigData.Hash, []byte(sigData.Key), SignatureHeader, SignatureHeaders)
	}

	return &httpUpstreamProxy{
		upstream:  upstream.ID,
		handler:   proxy,
		wsHandler: wsProxy,
		auth:      auth,
	}
}

// httpUpstreamProxy represents a single HTTP(S) upstream proxy
type httpUpstreamProxy struct {
	upstream  string
	handler   http.Handler
	wsHandler http.Handler
	auth      hmacauth.HmacAuth
}

// ServeHTTP proxies requests to the upstream provider while signing the
// request headers
func (h *httpUpstreamProxy) ServeHTTP(rw http.ResponseWriter, req *http.Request) {
	scope := middleware.GetRequestScope(req)
	// If scope is nil, this will panic.
	// A scope should always be injected before this handler is called.
	scope.Upstream = h.upstream

	// TODO (@NickMeves) - Deprecate GAP-Signature & remove GAP-Auth
	if h.auth != nil {
		req.Header.Set("GAP-Auth", rw.Header().Get("GAP-Auth"))
		h.auth.SignRequest(req)
	}
	if h.wsHandler != nil && strings.EqualFold(req.Header.Get("Connection"), "upgrade") && req.Header.Get("Upgrade") == "websocket" {
		h.wsHandler.ServeHTTP(rw, req)
	} else {
		h.handler.ServeHTTP(rw, req)
	}
}

// newReverseProxy creates a new reverse proxy for proxying requests to upstream
// servers based on the upstream configuration provided.
// The proxy should render an error page if there are failures connecting to the
// upstream server.
func newReverseProxy(target *url.URL, upstream options.Upstream, errorHandler ProxyErrorHandler) http.Handler {
	proxy := httputil.NewSingleHostReverseProxy(target)

	// Configure options on the SingleHostReverseProxy
	if upstream.FlushInterval != nil {
		proxy.FlushInterval = upstream.FlushInterval.Duration()
	} else {
		proxy.FlushInterval = options.DefaultUpstreamFlushInterval
	}

        proxy.Transport = &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   0 * time.Second,
			KeepAlive: 0 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
        }

	// InsecureSkipVerify is a configurable option we allow
	/* #nosec G402 */
	if upstream.InsecureSkipTLSVerify {
		proxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}

	// Ensure we always pass the original request path
	setProxyDirector(proxy)

	if upstream.PassHostHeader != nil && !*upstream.PassHostHeader {
		setProxyUpstreamHostHeader(proxy, target)
	}

	// Set the error handler so that upstream connection failures render the
	// error page instead of sending a empty response
	if errorHandler != nil {
		proxy.ErrorHandler = errorHandler
	}
	return proxy
}

// setProxyUpstreamHostHeader sets the proxy.Director so that upstream requests
// receive a host header matching the target URL.
func setProxyUpstreamHostHeader(proxy *httputil.ReverseProxy, target *url.URL) {
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		req.Host = target.Host
	}
}

// setProxyDirector sets the proxy.Director so that request URIs are escaped
// when proxying to usptream servers.
func setProxyDirector(proxy *httputil.ReverseProxy) {
	director := proxy.Director
	proxy.Director = func(req *http.Request) {
		director(req)
		// use RequestURI so that we aren't unescaping encoded slashes in the request path
		req.URL.Opaque = req.RequestURI
		req.URL.RawQuery = ""
		req.URL.ForceQuery = false
	}
}

// newWebSocketReverseProxy creates a new reverse proxy for proxying websocket connections.
func newWebSocketReverseProxy(u *url.URL, skipTLSVerify bool) http.Handler {
	wsProxy := httputil.NewSingleHostReverseProxy(u)
	/* #nosec G402 */
	if skipTLSVerify {
		wsProxy.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	return wsProxy
}
