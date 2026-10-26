package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	stdmime "mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/Theyashsawarkar/sandesh/internal/mime"
	"github.com/Theyashsawarkar/sandesh/internal/store"
)

// MaxAttach is the most a message may carry: Gmail's own limit is 25 MB,
// and Chat's 200 MB, but more than this belongs in Drive.
const MaxAttach = 25 << 20

// DownloadDir is where saved attachments go: $SANDESH_DOWNLOADS, else
// the XDG download folder (or ~/Downloads), in a "sandesh" folder.
func DownloadDir() string {
	if d := os.Getenv("SANDESH_DOWNLOADS"); d != "" {
		return d
	}
	d := os.Getenv("XDG_DOWNLOAD_DIR")
	if d == "" {
		home, _ := os.UserHomeDir()
		d = filepath.Join(home, "Downloads")
	}
	return filepath.Join(d, "sandesh")
}

// writeDownload saves data as name in dir, never overwriting: a clash
// becomes "name (2).ext". Returns the path.
func writeDownload(dir, name string, data []byte) (string, error) {
	if dir == "" {
		dir = DownloadDir()
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name = mime.SafeName(name)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		p := filepath.Join(dir, name)
		if i > 1 {
			p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", stem, i, ext))
		}
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, err = f.Write(data)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(p)
			return "", err
		}
		return p, nil
	}
	return "", errors.New("too many files with that name")
}

// SaveMailAttachment downloads the n-th attachment (0-based) of a mail
// into dir ("" = DownloadDir) and returns where it went.
func (a *App) SaveMailAttachment(ctx context.Context, account, msgID string, n int, dir string) (string, error) {
	c, err := a.Client(account)
	if err != nil {
		return "", err
	}
	gm, err := c.GetMessage(ctx, msgID)
	if err != nil {
		return "", err
	}
	name, _, id, _, ok := mime.FindAttachment(gm.Payload, n)
	if !ok {
		return "", fmt.Errorf("message %s has no attachment %d", msgID, n+1)
	}
	data, err := c.AttachmentData(ctx, msgID, id)
	if err != nil {
		return "", err
	}
	return writeDownload(dir, name, data)
}

// SaveChatAttachment downloads the n-th file of a chat message. Drive
// files can't be downloaded here (that needs Drive access): the error
// says to open them in the browser.
func (a *App) SaveChatAttachment(ctx context.Context, account, msgName string, n int, dir string) (string, error) {
	m, err := a.Store.ChatMessage(account, msgName)
	if err != nil {
		return "", fmt.Errorf("no chat message %s here", msgName)
	}
	if n < 0 || n >= len(m.Files) {
		return "", fmt.Errorf("that message has no file %d", n+1)
	}
	f := m.Files[n]
	if f.Ref == "" {
		return "", fmt.Errorf("%s is a Drive file: open it in Chat (b)", f.Name)
	}
	c, err := a.Client(account)
	if err != nil {
		return "", err
	}
	data, err := c.ChatMedia(ctx, f.Ref)
	if err != nil {
		return "", err
	}
	return writeDownload(dir, f.Name, data)
}

// AttachFiles makes private copies (0600, under the state folder) of the
// files to send, so the bytes you approved are the bytes that go out.
func (a *App) AttachFiles(paths []string) ([]store.File, error) {
	var fs []store.File
	for _, p := range paths {
		fs = append(fs, store.File{Source: p})
	}
	return a.ResolveFiles(fs)
}

// ResolveFiles copies every file that has no private copy yet (Path ""),
// keeping the ones that do. The total stays under MaxAttach.
func (a *App) ResolveFiles(files []store.File) ([]store.File, error) {
	var keep []store.File
	var paths []string
	var total int64
	for _, f := range files {
		if f.Path != "" {
			keep = append(keep, f)
			total += f.Size
		} else {
			paths = append(paths, f.Source)
		}
	}
	if len(paths) == 0 {
		return keep, nil
	}
	fresh, err := a.copyFiles(paths, total)
	if err != nil {
		return nil, err
	}
	return append(keep, fresh...), nil
}

func (a *App) copyFiles(paths []string, total int64) ([]store.File, error) {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	dir := filepath.Join(a.Paths.State, "outbox", hex.EncodeToString(b))
	var out []store.File
	fail := func(err error) ([]store.File, error) {
		os.RemoveAll(dir)
		return nil, err
	}
	for _, typed := range paths {
		p := ExpandPath(typed)
		st, err := os.Stat(p)
		if err != nil {
			return fail(err)
		}
		if !st.Mode().IsRegular() {
			return fail(fmt.Errorf("%s is not a file", p))
		}
		if total += st.Size(); total > MaxAttach {
			return fail(fmt.Errorf("attachments are over %d MB: share big files from Drive", MaxAttach>>20))
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fail(err)
		}
		src, err := os.Open(p)
		if err != nil {
			return fail(err)
		}
		name := mime.SafeName(filepath.Base(p))
		dst := filepath.Join(dir, fmt.Sprintf("%d-%s", len(out), name))
		f, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			src.Close()
			return fail(err)
		}
		head := make([]byte, 512)
		k, _ := io.ReadFull(src, head)
		_, _ = src.Seek(0, io.SeekStart)
		_, err = io.Copy(f, src)
		src.Close()
		f.Close()
		if err != nil {
			return fail(err)
		}
		ct := stdmime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
		if ct == "" {
			ct = http.DetectContentType(head[:k])
		}
		out = append(out, store.File{Name: name, Type: ct, Size: st.Size(), Path: dst, Source: typed})
	}
	return out, nil
}

// dropFiles removes the private copies once a send is over.
func (a *App) dropFiles(files []store.File) {
	root := filepath.Join(a.Paths.State, "outbox")
	for _, f := range files {
		d := filepath.Dir(f.Path)
		if strings.HasPrefix(d, root+string(os.PathSeparator)) {
			os.RemoveAll(d)
		}
	}
}

func readFiles(files []store.File) ([]mime.File, error) {
	var out []mime.File
	for _, f := range files {
		data, err := os.ReadFile(f.Path)
		if err != nil {
			return nil, fmt.Errorf("attachment %s: %w", f.Name, err)
		}
		out = append(out, mime.File{Name: f.Name, Type: f.Type, Data: data})
	}
	return out, nil
}

// uploadChat uploads the files to a space for the message carrying them.
func (a *App) uploadChat(ctx context.Context, account, space string, files []store.File) ([]json.RawMessage, error) {
	if len(files) == 0 {
		return nil, nil
	}
	c, err := a.Client(account)
	if err != nil {
		return nil, err
	}
	data, err := readFiles(files)
	if err != nil {
		return nil, err
	}
	var refs []json.RawMessage
	for _, f := range data {
		ref, err := c.UploadChatFile(ctx, space, f.Name, f.Type, f.Data)
		if err != nil {
			return nil, fmt.Errorf("upload %s: %w", f.Name, err)
		}
		refs = append(refs, ref)
	}
	return refs, nil
}

// ExpandPath expands ~ and $VARS in a path someone typed.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		p = filepath.Join(home, p[1:])
	}
	return os.ExpandEnv(p)
}

// HumanSize is 1.2 MB, 340 KB, 12 B.
func HumanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d B", n)
}

// SaveThreadAttachments downloads a thread's attachments, numbered across
// the thread from 1 as `mail read` shows them (n = 0: all of them).
func (a *App) SaveThreadAttachments(ctx context.Context, thread string, n int, dir string) ([]string, error) {
	t, err := a.Store.FindThread(thread)
	if err != nil {
		return nil, err
	}
	msgs, err := a.Store.Messages(t.Account, t.ID)
	if err != nil {
		return nil, err
	}
	var saved []string
	k := 0
	for _, m := range msgs {
		for i := range m.Attachments {
			k++
			if n != 0 && k != n {
				continue
			}
			p, err := a.SaveMailAttachment(ctx, t.Account, m.ID, i, dir)
			if err != nil {
				return saved, err
			}
			saved = append(saved, p)
		}
	}
	if len(saved) == 0 {
		if n == 0 {
			return nil, errors.New("that thread has no attachments")
		}
		return nil, fmt.Errorf("that thread has no attachment %d", n)
	}
	return saved, nil
}

// SaveChatFiles downloads a chat message's files (n from 1; 0 = all).
func (a *App) SaveChatFiles(ctx context.Context, msgName string, n int, dir string) ([]string, error) {
	for _, acc := range a.AccountNames() {
		m, err := a.Store.ChatMessage(acc, msgName)
		if err != nil {
			continue
		}
		var saved []string
		for i := range m.Files {
			if n != 0 && i+1 != n {
				continue
			}
			p, err := a.SaveChatAttachment(ctx, acc, m.Name, i, dir)
			if err != nil {
				return saved, err
			}
			saved = append(saved, p)
		}
		if len(saved) == 0 {
			return nil, errors.New("that message has no such file")
		}
		return saved, nil
	}
	return nil, fmt.Errorf("no chat message %s in the local copy", msgName)
}
