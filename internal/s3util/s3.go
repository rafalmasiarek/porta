package s3util

import (
    "bytes"
    "context"
    "crypto/hmac"
    "crypto/sha256"
    "encoding/hex"
    "encoding/xml"
    "fmt"
    "io"
    "net/http"
    "net/url"
    "os"
    "path"
    "path/filepath"
    "sort"
    "strings"
    "time"

    "github.com/rafalmasiarek/porta/internal/config"
)

type Client struct {
    Endpoint  *url.URL
    Bucket    string
    Prefix    string
    Region    string
    AccessKey string
    SecretKey string
    PathStyle bool
    HTTP      *http.Client
}

type ObjectInfo struct {
    Key  string
    Size int64
}

type listBucketResult struct {
    Contents []struct {
        Key  string `xml:"Key"`
        Size int64  `xml:"Size"`
    } `xml:"Contents"`
}

func New(cfg config.Storage) (*Client, error) {
    if cfg.Bucket == "" || cfg.Endpoint == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
        return nil, fmt.Errorf("storage config is incomplete")
    }
    u, err := url.Parse(cfg.Endpoint)
    if err != nil {
        return nil, err
    }
    if u.Scheme == "" {
        if cfg.UseSSL != nil && !*cfg.UseSSL {
            u.Scheme = "http"
        } else {
            u.Scheme = "https"
        }
    }
    return &Client{
        Endpoint: u,
        Bucket: cfg.Bucket,
        Prefix: strings.Trim(cfg.Prefix, "/"),
        Region: cfg.Region,
        AccessKey: cfg.AccessKey,
        SecretKey: cfg.SecretKey,
        PathStyle: cfg.PathStyle,
        HTTP: &http.Client{Timeout: 0},
    }, nil
}

func (c *Client) Join(parts ...string) string {
    items := make([]string, 0, len(parts)+1)
    if c.Prefix != "" {
        items = append(items, c.Prefix)
    }
    for _, p := range parts {
        if p != "" {
            items = append(items, strings.Trim(p, "/"))
        }
    }
    return path.Join(items...)
}

func (c *Client) Exists(ctx context.Context, key string) (bool, error) {
    req, err := c.newRequest(ctx, http.MethodHead, key, nil, 0, "", false)
    if err != nil {
        return false, err
    }
    resp, err := c.HTTP.Do(req)
    if err != nil {
        return false, err
    }
    defer resp.Body.Close()
    if resp.StatusCode == 404 {
        return false, nil
    }
    if resp.StatusCode >= 300 {
        return false, fmt.Errorf("HEAD %s returned %s", key, resp.Status)
    }
    return true, nil
}

func (c *Client) UploadFile(ctx context.Context, localPath, key string) error {
    f, err := os.Open(localPath)
    if err != nil {
        return err
    }
    defer f.Close()
    st, err := f.Stat()
    if err != nil {
        return err
    }
    hash, err := hashFile(localPath)
    if err != nil {
        return err
    }
    req, err := c.newRequest(ctx, http.MethodPut, key, f, st.Size(), hash, true)
    if err != nil {
        return err
    }
    resp, err := c.HTTP.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return fmt.Errorf("PUT %s returned %s: %s", key, resp.Status, string(body))
    }
    return nil
}

func (c *Client) UploadBytes(ctx context.Context, b []byte, key, contentType string) error {
    hash := sha256Hex(b)
    req, err := c.newRequest(ctx, http.MethodPut, key, bytes.NewReader(b), int64(len(b)), hash, true)
    if err != nil {
        return err
    }
    if contentType != "" {
        req.Header.Set("Content-Type", contentType)
    }
    resp, err := c.HTTP.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return fmt.Errorf("PUT %s returned %s: %s", key, resp.Status, string(body))
    }
    return nil
}

func (c *Client) DownloadToFile(ctx context.Context, key, localPath string) error {
    req, err := c.newRequest(ctx, http.MethodGet, key, nil, 0, "", false)
    if err != nil {
        return err
    }
    resp, err := c.HTTP.Do(req)
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return fmt.Errorf("GET %s returned %s: %s", key, resp.Status, string(body))
    }
    if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
        return err
    }
    tmp := localPath + ".tmp"
    f, err := os.Create(tmp)
    if err != nil {
        return err
    }
    if _, err := io.Copy(f, resp.Body); err != nil {
        f.Close()
        return err
    }
    if err := f.Close(); err != nil {
        return err
    }
    return os.Rename(tmp, localPath)
}

func (c *Client) ReadAll(ctx context.Context, key string) ([]byte, error) {
    req, err := c.newRequest(ctx, http.MethodGet, key, nil, 0, "", false)
    if err != nil {
        return nil, err
    }
    resp, err := c.HTTP.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return nil, fmt.Errorf("GET %s returned %s: %s", key, resp.Status, string(body))
    }
    return io.ReadAll(resp.Body)
}

func (c *Client) List(ctx context.Context, prefix string, recursive bool) ([]ObjectInfo, error) {
    q := url.Values{}
    q.Set("list-type", "2")
    q.Set("prefix", prefix)
    if !recursive {
        q.Set("delimiter", "/")
    }
    req, err := c.newRequest(ctx, http.MethodGet, "", nil, 0, "", false)
    if err != nil {
        return nil, err
    }
    req.URL.RawQuery = q.Encode()
    c.sign(req, sha256Hex(nil))
    resp, err := c.HTTP.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    if resp.StatusCode >= 300 {
        body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
        return nil, fmt.Errorf("LIST returned %s: %s", resp.Status, string(body))
    }
    var result listBucketResult
    if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
        return nil, err
    }
    out := make([]ObjectInfo, 0, len(result.Contents))
    for _, v := range result.Contents {
        out = append(out, ObjectInfo{Key: v.Key, Size: v.Size})
    }
    sort.Slice(out, func(i,j int) bool { return out[i].Key < out[j].Key })
    return out, nil
}

func (c *Client) newRequest(ctx context.Context, method, key string, body io.Reader, size int64, payloadHash string, setLength bool) (*http.Request, error) {
    u := *c.Endpoint
    if c.PathStyle {
        u.Path = path.Join(u.Path, c.Bucket)
    } else {
        u.Host = c.Bucket + "." + u.Host
    }
    if key != "" {
        u.Path = path.Join(u.Path, key)
    }
    req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
    if err != nil {
        return nil, err
    }
    if setLength {
        req.ContentLength = size
    }
    if payloadHash == "" {
        payloadHash = sha256Hex(nil)
    }
    c.sign(req, payloadHash)
    return req, nil
}

func (c *Client) sign(req *http.Request, payloadHash string) {
    now := time.Now().UTC()
    amzDate := now.Format("20060102T150405Z")
    dateStamp := now.Format("20060102")
    req.Header.Set("x-amz-date", amzDate)
    req.Header.Set("x-amz-content-sha256", payloadHash)
    host := req.URL.Host
    req.Header.Set("Host", host)

    canonicalURI := req.URL.EscapedPath()
    canonicalQuery := canonicalQuery(req.URL.Query())
    signedHeaders := "host;x-amz-content-sha256;x-amz-date"
    canonicalHeaders := "host:" + host + "\n" +
        "x-amz-content-sha256:" + payloadHash + "\n" +
        "x-amz-date:" + amzDate + "\n"

    canonicalRequest := strings.Join([]string{
        req.Method,
        canonicalURI,
        canonicalQuery,
        canonicalHeaders,
        signedHeaders,
        payloadHash,
    }, "\n")

    credentialScope := dateStamp + "/" + c.Region + "/s3/aws4_request"
    stringToSign := strings.Join([]string{
        "AWS4-HMAC-SHA256",
        amzDate,
        credentialScope,
        sha256Hex([]byte(canonicalRequest)),
    }, "\n")

    signingKey := deriveSigningKey(c.SecretKey, dateStamp, c.Region, "s3")
    signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
    authHeader := "AWS4-HMAC-SHA256 Credential=" + c.AccessKey + "/" + credentialScope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
    req.Header.Set("Authorization", authHeader)
}

func canonicalQuery(v url.Values) string {
    if len(v) == 0 {
        return ""
    }
    keys := make([]string, 0, len(v))
    for k := range v {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    parts := make([]string, 0)
    for _, k := range keys {
        vals := append([]string(nil), v[k]...)
        sort.Strings(vals)
        for _, val := range vals {
            parts = append(parts, sigEscape(k)+"="+sigEscape(val))
        }
    }
    return strings.Join(parts, "&")
}

func sigEscape(s string) string {
    e := url.QueryEscape(s)
    e = strings.ReplaceAll(e, "+", "%20")
    e = strings.ReplaceAll(e, "*", "%2A")
    e = strings.ReplaceAll(e, "%7E", "~")
    return e
}

func deriveSigningKey(secret, date, region, service string) []byte {
    kDate := hmacSHA256([]byte("AWS4"+secret), date)
    kRegion := hmacSHA256(kDate, region)
    kService := hmacSHA256(kRegion, service)
    return hmacSHA256(kService, "aws4_request")
}

func hmacSHA256(key []byte, data string) []byte {
    h := hmac.New(sha256.New, key)
    _, _ = h.Write([]byte(data))
    return h.Sum(nil)
}

func sha256Hex(data []byte) string {
    sum := sha256.Sum256(data)
    return hex.EncodeToString(sum[:])
}

func hashFile(path string) (string, error) {
    f, err := os.Open(path)
    if err != nil {
        return "", err
    }
    defer f.Close()
    h := sha256.New()
    if _, err := io.Copy(h, f); err != nil {
        return "", err
    }
    return hex.EncodeToString(h.Sum(nil)), nil
}

func Retry(ctx context.Context, attempts int, fn func() error) error {
    var err error
    for i:=1;i<=attempts;i++ {
        err = fn()
        if err == nil {
            return nil
        }
        if i < attempts {
            select {
            case <-ctx.Done():
                return ctx.Err()
            case <-time.After(time.Duration(i*i) * time.Second):
            }
        }
    }
    return err
}
