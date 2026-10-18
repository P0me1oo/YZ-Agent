package agentcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const releaseMetadataURL = "https://api.github.com/repos/P0me1oo/YZ-Agent/releases/latest"

var releaseMirrors = []string{
	"https://gh-proxy.org/",
	"https://ghfast.top/",
	"https://github.chenc.dev/",
}

var checksumLinePattern = regexp.MustCompile(`(?m)^[a-fA-F0-9]{64}[ \t]+\*?[^\r\n]+\r?$`)
var errReleaseChecksumsUnavailable = errors.New("所有来源均无法提供校验清单")

// 一次升级共用网络状态，直连超时后不再为后续附件重复等待。
type releaseDownloader struct {
	client       *http.Client
	mirrors      []string
	probeTimeout time.Duration
	mirrorOnly   bool
}

type mirrorProbe struct {
	base    string
	elapsed time.Duration
	data    []byte
	err     error
}

func newReleaseDownloader() *releaseDownloader {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &releaseDownloader{
		client:       &http.Client{Transport: transport, Timeout: 120 * time.Second},
		mirrors:      releaseMirrors,
		probeTimeout: 5 * time.Second,
	}
}

func downloadTimedOut(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err)
}

// 使用实际版本接口、校验清单或程序文件的前 4 KiB，避免主页可达却无法下载的误判。
// 部分镜像不支持 HEAD；GET 探测时间包含 TLS 和响应内容读取。
func (d *releaseDownloader) probeMirrors(ctx context.Context, url string, validate func([]byte) error) ([]mirrorProbe, error) {
	results := make(chan mirrorProbe, len(d.mirrors))
	for _, base := range d.mirrors {
		go func(base string) {
			start := time.Now()
			probeCtx, cancel := context.WithTimeout(ctx, d.probeTimeout)
			defer cancel()
			data, err := readReleaseResponse(probeCtx, d.client, base+url)
			if err == nil && validate != nil {
				err = validate(data)
			}
			results <- mirrorProbe{base: base, elapsed: time.Since(start), data: data, err: err}
		}(base)
	}
	var available []mirrorProbe
	var failures []error
	for range d.mirrors {
		result := <-results
		if result.err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", result.base, result.err))
		} else {
			available = append(available, result)
		}
	}
	sort.SliceStable(available, func(i, j int) bool {
		if available[i].elapsed == available[j].elapsed {
			return available[i].base < available[j].base
		}
		return available[i].elapsed < available[j].elapsed
	})
	if len(available) == 0 {
		return nil, fmt.Errorf("没有可用的 GitHub 镜像源：%w", errors.Join(failures...))
	}
	return available, nil
}

func readReleaseResponse(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	sample := strings.Contains(url, "/releases/download/") && !strings.HasSuffix(url, "/SHA256SUMS")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if sample {
		req.Header.Set("Range", "bytes=0-4095")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && !(sample && resp.StatusCode == http.StatusPartialContent) {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	limit := int64(1 << 20)
	if sample {
		limit = 4096
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if !sample && int64(len(data)) > limit {
		return nil, errors.New("版本信息或校验清单超过大小限制")
	}
	if sample && (len(data) == 0 || strings.HasPrefix(strings.TrimSpace(string(data)), "<") || strings.Contains(resp.Header.Get("Content-Type"), "text/html")) {
		return nil, errors.New("镜像未返回程序文件")
	}
	return data, nil
}

func writeReleaseData(dest string, data []byte) error {
	defer os.Remove(dest + ".part")
	if err := os.WriteFile(dest+".part", data, 0o600); err != nil {
		return err
	}
	return os.Rename(dest+".part", dest)
}

func (d *releaseDownloader) downloadChecksums(url, dest string) error {
	validate := func(data []byte) error {
		if !checksumLinePattern.Match(data) {
			return errors.New("未返回有效的校验清单")
		}
		return nil
	}
	if !d.mirrorOnly {
		data, err := readReleaseResponse(context.Background(), d.client, url)
		if err == nil && validate(data) == nil {
			return writeReleaseData(dest, data)
		}
		if downloadTimedOut(err) {
			d.mirrorOnly = true
		}
		fmt.Println("GitHub 校验清单不可用，正在探测备用下载源...")
	}
	available, err := d.probeMirrors(context.Background(), url, validate)
	if err != nil {
		return fmt.Errorf("%w：%w", errReleaseChecksumsUnavailable, err)
	}
	selected := available[0]
	fmt.Printf("使用镜像源 %s（HTTPS 响应 %d 毫秒）\n", selected.base, selected.elapsed.Milliseconds())
	return writeReleaseData(dest, selected.data)
}

func parseLatestReleaseMetadata(data []byte) (string, error) {
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      *bool  `json:"draft"`
		Prerelease *bool  `json:"prerelease"`
	}
	if err := json.Unmarshal(data, &release); err != nil {
		return "", fmt.Errorf("版本接口返回无效内容：%w", err)
	}
	if release.Draft == nil || release.Prerelease == nil || *release.Draft || *release.Prerelease || !releaseVersionPattern.MatchString(release.Tag) {
		return "", errors.New("版本接口未返回有效的正式 Release")
	}
	return release.Tag, nil
}

func (d *releaseDownloader) latestStableRelease(ctx context.Context) (string, error) {
	if !d.mirrorOnly {
		queryCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		target, err := queryLatestRelease(queryCtx, d.client)
		cancel()
		if err == nil || !downloadTimedOut(err) || ctx.Err() != nil {
			return target, err
		}
		d.mirrorOnly = true
		fmt.Println("GitHub 查询超时，正在探测备用下载源...")
	}
	available, err := d.probeMirrors(ctx, releaseMetadataURL, func(data []byte) error {
		_, err := parseLatestReleaseMetadata(data)
		return err
	})
	if err != nil {
		return "", err
	}
	selected := available[0]
	fmt.Printf("使用镜像源 %s（HTTPS 响应 %d 毫秒）\n", selected.base, selected.elapsed.Milliseconds())
	return parseLatestReleaseMetadata(selected.data)
}

func (d *releaseDownloader) download(url, dest string) error {
	if strings.HasSuffix(url, "/SHA256SUMS") {
		return d.downloadChecksums(url, dest)
	}
	if !d.mirrorOnly {
		err := downloadFileWithClient(d.client, url, dest)
		if err == nil || !downloadTimedOut(err) {
			return err
		}
		d.mirrorOnly = true
		fmt.Println("GitHub 下载超时，正在探测备用下载源...")
	}
	available, err := d.probeMirrors(context.Background(), url, nil)
	if err != nil {
		return err
	}
	var failures []error
	for _, source := range available {
		fmt.Printf("使用镜像源 %s（HTTPS 响应 %d 毫秒）\n", source.base, source.elapsed.Milliseconds())
		if err := downloadFileWithClient(d.client, source.base+url, dest); err != nil {
			var localError *os.PathError
			if errors.As(err, &localError) {
				return err
			}
			failures = append(failures, fmt.Errorf("%s: %w", source.base, err))
			continue
		}
		return nil
	}
	return fmt.Errorf("所有可用镜像下载均失败：%w", errors.Join(failures...))
}
