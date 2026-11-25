package r2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Minimal AWS Signature Version 4 (header auth) for the S3 backend: the
// three object calls it makes need nothing else, and it keeps an SDK out of
// hostd. Tested against AWS's published S3 example.

const (
	emptySHA256     = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	amzDateFormat   = "20060102T150405Z"
)

// Credentials sign S3 requests.
type Credentials struct {
	AccessKeyID, SecretAccessKey, SessionToken string
}

// signV4 signs req for service in region at now. payloadHash is the hex
// SHA-256 of the body, or "UNSIGNED-PAYLOAD". It sets Host-derived,
// x-amz-date, x-amz-content-sha256 (and x-amz-security-token) headers and
// Authorization. Signed headers: host, range, content-md5, content-type
// and every x-amz-* header present.
func signV4(req *http.Request, c Credentials, region, service, payloadHash string, now time.Time) {
	t := now.UTC()
	amzDate := t.Format(amzDateFormat)
	day := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if c.SessionToken != "" {
		req.Header.Set("x-amz-security-token", c.SessionToken)
	}
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}

	hdrs := map[string]string{"host": host}
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "range" || lk == "content-md5" || lk == "content-type" {
			for i := range vs {
				vs[i] = strings.Join(strings.Fields(vs[i]), " ")
			}
			hdrs[lk] = strings.Join(vs, ",")
		}
	}
	names := make([]string, 0, len(hdrs))
	for k := range hdrs {
		names = append(names, k)
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, k := range names {
		ch.WriteString(k + ":" + hdrs[k] + "\n")
	}
	signed := strings.Join(names, ";")

	canon := strings.Join([]string{
		req.Method,
		canonicalPath(req.URL),
		canonicalQuery(req.URL.Query()),
		ch.String(),
		signed,
		payloadHash,
	}, "\n")
	scope := day + "/" + region + "/" + service + "/aws4_request"
	sts := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA256([]byte(canon))
	k := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), day)
	k = hmacSHA256(k, region)
	k = hmacSHA256(k, service)
	k = hmacSHA256(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(k, sts))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+c.AccessKeyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

// canonicalPath is the URI-encoded path (S3: each segment encoded once).
func canonicalPath(u *url.URL) string {
	p := u.Path
	if p == "" {
		return "/"
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = awsEscape(s)
	}
	return strings.Join(segs, "/")
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, awsEscape(k)+"="+awsEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// awsEscape percent-encodes everything but A-Z a-z 0-9 - _ . ~.
func awsEscape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

func hmacSHA256(key []byte, s string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(s))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// VerifyV4 checks req's SigV4 header signature against c (for test S3
// servers): it re-signs a copy with the request's own date and payload
// hash and compares. It doesn't check the clock or the payload hash.
func VerifyV4(req *http.Request, c Credentials, region string) error {
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential="+c.AccessKeyID+"/") {
		return errors.New("sigv4: missing or foreign credential")
	}
	t, err := time.Parse(amzDateFormat, req.Header.Get("x-amz-date"))
	if err != nil {
		return errors.New("sigv4: bad x-amz-date")
	}
	_, signedList, _ := strings.Cut(auth, "SignedHeaders=")
	signedList, _, _ = strings.Cut(signedList, ",")
	cp := &http.Request{Method: req.Method, URL: &url.URL{Path: req.URL.Path, RawQuery: req.URL.RawQuery}, Host: req.Host, Header: http.Header{}}
	for _, h := range strings.Split(signedList, ";") {
		if h != "host" {
			if v, ok := req.Header[http.CanonicalHeaderKey(h)]; ok {
				cp.Header[http.CanonicalHeaderKey(h)] = append([]string(nil), v...)
			}
		}
	}
	signV4(cp, c, region, "s3", req.Header.Get("x-amz-content-sha256"), t)
	if cp.Header.Get("Authorization") != auth {
		return errors.New("sigv4: signature mismatch")
	}
	return nil
}
