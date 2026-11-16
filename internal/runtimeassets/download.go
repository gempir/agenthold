package runtimeassets

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

// downloader fetches the compressed base image. The transfer survives dropped connections, slow
// links and process restarts: bytes accumulate in dir/.<sha256>.ext4.zst.part and are requested
// with Range (guarded by If-Range) until the file is complete. Only then is the pinned checksum
// checked. The idle limit counts time without a single new byte, never total time, so a slow but
// healthy uplink does not fail.
type downloader struct {
	client *http.Client
	log    *slog.Logger
	// stall aborts one attempt when no bytes arrive for this long.
	stall time.Duration
	// giveUp ends the retries after this long without progress.
	giveUp time.Duration
	// backoffMin and backoffMax bound the wait between attempts.
	backoffMin, backoffMax time.Duration
	// progress is how often progress is logged.
	progress time.Duration
}

// tune lets tests shorten the timings.
var tune func(*downloader)

func newDownloader(client *http.Client, l *slog.Logger) *downloader {
	d := &downloader{
		client: client, log: log(l).With("component", "base-download"),
		stall: 90 * time.Second, giveUp: 30 * time.Minute,
		backoffMin: time.Second, backoffMax: 30 * time.Second, progress: 5 * time.Second,
	}
	if tune != nil {
		tune(d)
	}
	return d
}

// fetchBase makes path (the verified uncompressed base) exist, from a kept or fetched compressed
// image. Sources (see Options.candidates) are only resolved when a download is needed and are
// tried in order; the first complete, verified one wins.
func (d *downloader) fetchBase(ctx context.Context, path string, sources func() ([]string, error), b baseArtifact) error {
	dir := filepath.Dir(path)
	unlock, err := lockBase(ctx, dir, b.SHA256)
	if err != nil {
		return err
	}
	defer unlock()
	// A second process (or the previous run) may have finished while we waited for the lock.
	if ok, err := verifyFile(ctx, path, b.artifact); err != nil && !errors.Is(err, errChecksum) {
		return err
	} else if ok {
		return os.Chmod(path, 0o644)
	}
	removeStaleParts(dir, b.SHA256)
	zst := path + ".zst"
	ok, err := verifyFile(ctx, zst, b.Compressed)
	switch {
	case err != nil && !errors.Is(err, errChecksum):
		return err
	case errors.Is(err, errChecksum):
		_ = os.Remove(zst) // wrong content under our name: it is replaced, never trusted
	}
	if !ok {
		urls, err := sources()
		if err != nil {
			return err
		}
		var errs []error
		for _, u := range urls {
			err := d.fetch(ctx, u, zst, b)
			if err == nil {
				errs = nil
				break
			}
			if ctx.Err() != nil {
				return err
			}
			errs = append(errs, fmt.Errorf("%s: %w", u, err))
			d.log.Warn("base download failed", "url", u, "err", err)
		}
		if errs != nil {
			return errors.Join(errs...)
		}
	}
	d.log.Info("decompressing base image", "from", zst, "to", path)
	return decompress(ctx, zst, path, b.artifact)
}

func decompress(ctx context.Context, zst, path string, want artifact) error {
	compressed, err := os.Open(zst)
	if err != nil {
		return err
	}
	defer compressed.Close()
	decoder, err := zstd.NewReader(contextReader{ctx, compressed}, zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(256<<20), zstd.WithDecoderMaxWindow(128<<20))
	if err != nil {
		return err
	}
	defer decoder.Close()
	return atomicWrite(ctx, path, decoder, want, 0o644)
}

// fetch downloads url into dest (dir/<sha256>.ext4.zst) and checks it against want.
func (d *downloader) fetch(ctx context.Context, url, dest string, b baseArtifact) error {
	want := b.Compressed
	part := partPath(filepath.Dir(dest), b.SHA256)
	t := &transfer{d: d, url: url, part: part, want: want, lastProgress: time.Now(), lastLog: time.Now()}
	backoff := d.backoffMin
	hashRestarts := 0
	for {
		progressed, err := t.attempt(ctx)
		if err == nil {
			ok, verr := verifyFile(ctx, part, want)
			if verr != nil && !errors.Is(verr, errChecksum) {
				return verr
			}
			if ok {
				return publish(part, dest)
			}
			// Appended bytes may have come from a different file than the first ones (a server
			// that changed under us): start over once before blaming the server.
			resumed := t.resumedFrom > 0
			discard(part)
			if !resumed || hashRestarts > 0 {
				return fmt.Errorf("downloaded image does not match the release checksum (size %d, sha256 %s): the server sent other bytes than this release pins", want.Size, want.SHA256)
			}
			hashRestarts++
			t.resumedFrom = 0
			d.log.Warn("resumed download failed its checksum; starting over", "url", url)
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err() // keep the part file: the next run resumes
		}
		var perm permanentError
		if errors.As(err, &perm) {
			return err
		}
		if progressed {
			backoff = d.backoffMin
		}
		if idle := time.Since(t.lastProgress); idle > d.giveUp {
			return fmt.Errorf("no progress for %s, giving up (the bytes so far are kept for the next start): %w", idle.Round(time.Second), err)
		}
		wait := backoff
		var ra retryAfterError
		if errors.As(err, &ra) && ra.d > wait {
			wait = min(ra.d, 5*time.Minute)
		}
		d.log.Warn("base download interrupted; retrying", "url", url, "have", t.have, "of", want.Size, "in", wait.Round(time.Millisecond).String(), "err", err)
		if !sleep(ctx, wait/2+rand.N(wait/2+1)) { //nolint:gosec // jitter
			return ctx.Err()
		}
		backoff = min(backoff*2, d.backoffMax)
	}
}

// transfer is one resumable download of one URL.
type transfer struct {
	d    *downloader
	url  string
	part string
	want artifact

	have         int64 // bytes in the part file
	resumedFrom  int64 // bytes that were already there when the first attempt started
	started      bool
	lastProgress time.Time
	lastLog      time.Time
}

type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

type retryAfterError struct {
	error
	d time.Duration
}

func (e retryAfterError) Unwrap() error { return e.error }

// attempt performs one request and appends what arrives. It returns nil once the part file holds
// want.Size bytes (checksum not yet checked). progressed reports whether new bytes arrived.
func (t *transfer) attempt(ctx context.Context) (progressed bool, err error) {
	fi, err := os.Lstat(t.part)
	have := int64(0)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return false, permanentError{err}
	case !fi.Mode().IsRegular():
		return false, permanentError{fmt.Errorf("%s is not a regular file", t.part)}
	default:
		have = fi.Size()
	}
	if have > t.want.Size {
		discard(t.part)
		have = 0
	}
	if !t.started {
		t.started, t.resumedFrom = true, have
		if have > 0 {
			t.d.log.Info("resuming base download", "url", t.url, "have", have, "of", t.want.Size)
		} else {
			t.d.log.Info("downloading base image", "url", t.url, "size", t.want.Size)
		}
	}
	t.have = have
	if have == t.want.Size {
		return false, nil
	}

	actx, cancel := context.WithCancel(ctx)
	defer cancel()
	stall := time.AfterFunc(t.d.stall, cancel)
	defer stall.Stop()

	req, err := http.NewRequestWithContext(actx, http.MethodGet, t.url, nil)
	if err != nil {
		return false, permanentError{err}
	}
	// No transparent decompression: the bytes on the wire are the pinned file.
	req.Header.Set("Accept-Encoding", "identity")
	validator := readValidator(t.part)
	if have > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(have, 10)+"-")
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	resp, err := t.d.client.Do(req)
	if err != nil {
		return false, t.wrap(ctx, err)
	}
	defer resp.Body.Close()
	stall.Reset(t.d.stall)

	flags := os.O_WRONLY | os.O_CREATE
	switch {
	case resp.StatusCode == http.StatusPartialContent:
		start, ok := contentRangeStart(resp.Header.Get("Content-Range"))
		if !ok || start != have {
			discard(t.part)
			return false, fmt.Errorf("server answered a Range request with Content-Range %q, wanted start %d", resp.Header.Get("Content-Range"), have)
		}
		flags |= os.O_APPEND
		t.resumedFrom = max(t.resumedFrom, have)
	case resp.StatusCode == http.StatusOK:
		// A plain 200 means the server ignored the range or the file changed (If-Range): start over.
		if have > 0 {
			t.d.log.Info("server restarted the transfer from zero", "url", t.url, "dropped", have)
		}
		flags |= os.O_TRUNC
		have = 0
		t.resumedFrom = 0
		saveValidator(t.part, validatorOf(resp))
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && have > 0:
		discard(t.part)
		t.resumedFrom = 0
		return false, errors.New("HTTP 416: the partial file does not fit the server's file; starting over")
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return false, retryAfterError{fmt.Errorf("HTTP %d", resp.StatusCode), retryAfter(resp)}
	default:
		return false, permanentError{fmt.Errorf("HTTP %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))}
	}
	if resp.StatusCode == http.StatusPartialContent && readValidator(t.part) == "" {
		saveValidator(t.part, validatorOf(resp))
	}
	f, err := os.OpenFile(t.part, flags, 0o600)
	if err != nil {
		return false, permanentError{err}
	}
	defer f.Close()
	t.have = have

	remaining := t.want.Size - have
	body := io.LimitReader(stallReader{resp.Body, stall, t.d.stall}, remaining+1)
	buf := make([]byte, 256<<10)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if int64(n) > t.want.Size-t.have {
				return progressed, permanentError{fmt.Errorf("server sent more than the %d bytes this release pins", t.want.Size)}
			}
			if _, werr := f.Write(buf[:n]); werr != nil {
				return progressed, permanentError{werr}
			}
			t.have += int64(n)
			progressed = true
			t.lastProgress = time.Now()
			t.logProgress()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return progressed, t.wrap(ctx, rerr)
		}
	}
	if t.have < t.want.Size {
		return progressed, io.ErrUnexpectedEOF
	}
	if err := f.Sync(); err != nil {
		return progressed, permanentError{err}
	}
	return progressed, nil
}

func (t *transfer) logProgress() {
	if time.Since(t.lastLog) < t.d.progress {
		return
	}
	t.lastLog = time.Now()
	t.d.log.Info("downloading base image", "bytes", t.have, "of", t.want.Size, "percent", int(100*t.have/t.want.Size))
}

// wrap turns an attempt cancelled by the stall timer into a retryable error.
func (t *transfer) wrap(ctx context.Context, err error) error {
	if ctx.Err() == nil && errors.Is(err, context.Canceled) {
		return fmt.Errorf("no data for %s: %w", t.d.stall, err)
	}
	return err
}

type stallReader struct {
	r io.Reader
	t *time.Timer
	d time.Duration
}

func (s stallReader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	if n > 0 {
		s.t.Reset(s.d)
	}
	return n, err
}

func contentRangeStart(h string) (int64, bool) {
	rest, ok := strings.CutPrefix(h, "bytes ")
	if !ok {
		return 0, false
	}
	first, _, ok := strings.Cut(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseInt(first, 10, 64)
	return n, err == nil && n >= 0
}

func retryAfter(resp *http.Response) time.Duration {
	if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	return 0
}

// validatorOf returns what If-Range may carry: a strong ETag, else Last-Modified.
func validatorOf(resp *http.Response) string {
	if e := resp.Header.Get("ETag"); e != "" && !strings.HasPrefix(e, "W/") {
		return e
	}
	return resp.Header.Get("Last-Modified")
}

func validatorPath(part string) string { return part + ".etag" }

func readValidator(part string) string {
	data, err := os.ReadFile(validatorPath(part))
	if err != nil || len(data) > 1024 {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveValidator(part, v string) {
	if v == "" || strings.ContainsAny(v, "\r\n") {
		_ = os.Remove(validatorPath(part))
		return
	}
	_ = os.WriteFile(validatorPath(part), []byte(v), 0o600)
}

func partPath(dir, sha string) string { return filepath.Join(dir, "."+sha+".ext4.zst.part") }

func discard(part string) {
	_ = os.Remove(part)
	_ = os.Remove(validatorPath(part))
}

// publish makes the verified part file the kept compressed image.
func publish(part, dest string) error {
	if err := os.Chmod(part, 0o644); err != nil {
		return err
	}
	if err := os.Rename(part, dest); err != nil {
		return err
	}
	_ = os.Remove(validatorPath(part))
	if dir, err := os.Open(filepath.Dir(dest)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// lockBase serializes preparation of one base image across processes sharing a data directory.
func lockBase(ctx context.Context, dir, sha string) (func(), error) {
	path := filepath.Join(dir, "."+sha+".lock")
	announced := false
	for {
		unlock, ok, err := tryLock(path)
		if err != nil {
			return nil, err
		}
		if ok {
			return unlock, nil
		}
		if !announced {
			slog.Info("another process is preparing the base image; waiting", "lock", path)
			announced = true
		}
		if !sleep(ctx, 500*time.Millisecond) {
			return nil, ctx.Err()
		}
	}
}

// removeStaleParts deletes partial downloads (and their lock files) of other releases that no
// process is working on.
func removeStaleParts(dir, keepSHA string) {
	parts, _ := filepath.Glob(filepath.Join(dir, ".*.ext4.zst.part"))
	for _, part := range parts {
		sha := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(part), "."), ".ext4.zst.part")
		if sha == keepSHA {
			continue
		}
		lock := filepath.Join(dir, "."+sha+".lock")
		unlock, ok, err := tryLock(lock)
		if err != nil || !ok {
			continue
		}
		discard(part)
		_ = os.Remove(lock)
		unlock()
	}
}
