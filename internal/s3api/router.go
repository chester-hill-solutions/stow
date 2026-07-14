package s3api

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

type routeInfo struct {
	bucket string
	key    string
	host   string
}

func parseRoute(r *http.Request, baseHost string) (routeInfo, s3Error) {
	host := hostWithoutPort(r.Host)
	info := routeInfo{host: host}

	path := r.URL.Path
	if path == "" {
		path = "/"
	}

	// Virtual-hosted-style: bucket.basehost
	if baseHost != "" && host != baseHost && strings.HasSuffix(host, "."+baseHost) {
		bucket := strings.TrimSuffix(host, "."+baseHost)
		if bucket == "" || strings.Contains(bucket, ".") {
			return info, s3Error{Code: "InvalidBucketName", Message: "Invalid bucket name", StatusCode: http.StatusBadRequest}
		}
		info.bucket = bucket
		info.key = strings.TrimPrefix(path, "/")
		if info.key != "" {
			key, err := url.PathUnescape(info.key)
			if err != nil {
				return info, s3Error{Code: "InvalidArgument", Message: "Invalid key", StatusCode: http.StatusBadRequest}
			}
			info.key = key
		}
		return info, s3Error{}
	}

	// Path-style
	trimmed := strings.TrimPrefix(path, "/")
	if trimmed == "" {
		return info, s3Error{}
	}
	parts := strings.SplitN(trimmed, "/", 2)
	info.bucket = parts[0]
	if len(parts) == 2 {
		key, err := url.PathUnescape(parts[1])
		if err != nil {
			return info, s3Error{Code: "InvalidArgument", Message: "Invalid key", StatusCode: http.StatusBadRequest}
		}
		info.key = key
	}
	return info, s3Error{}
}

func hostWithoutPort(hostport string) string {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return host
}

func resourcePath(bucket, key string) string {
	if key == "" {
		return "/" + bucket
	}
	return "/" + bucket + "/" + key
}

func urlEncodeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}
