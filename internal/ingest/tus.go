package ingest

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/configtheworld/media-digest/internal/naming"
	"github.com/configtheworld/media-digest/internal/store"
)

// This file implements the tus 1.0.0 resumable upload protocol (core,
// creation and termination extensions): https://tus.io/protocols/resumable-upload
// Uploads always belong to a planned batch item; the client never names files.

const (
	tusVersion = "1.0.0"
	// UploadPath is the URL prefix of upload resources.
	UploadPath = "/api/uploads/"
	// MaxChunk bounds a single PATCH body (the client sends 8 MiB chunks).
	MaxChunk = 256 << 20
	// readIdle is how long a PATCH body may stall before we give up on it.
	readIdle = 60 * time.Second
)

// UploadUser resolves the authenticated user for upload requests.
type UploadUser func(*http.Request) string

func tusHeaders(w http.ResponseWriter) {
	w.Header().Set("Tus-Resumable", tusVersion)
	w.Header().Set("Cache-Control", "no-store")
}

func tusError(w http.ResponseWriter, status int, msg string) {
	tusHeaders(w)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, msg+"\n")
}

// TusOptions answers protocol discovery.
func (s *Service) TusOptions(w http.ResponseWriter, r *http.Request) {
	tusHeaders(w)
	w.Header().Set("Tus-Version", tusVersion)
	w.Header().Set("Tus-Extension", "creation,termination")
	w.Header().Set("Tus-Max-Size", strconv.FormatInt(s.cfg.MaxUploadSize, 10))
	w.WriteHeader(http.StatusNoContent)
}

func checkTusVersion(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("Tus-Resumable") != tusVersion {
		w.Header().Set("Tus-Version", tusVersion)
		tusError(w, http.StatusPreconditionFailed, "unsupported tus version")
		return false
	}
	return true
}

// parseMetadata decodes "key base64,key base64".
func parseMetadata(h string) (map[string]string, bool) {
	out := map[string]string{}
	if strings.TrimSpace(h) == "" {
		return out, true
	}
	if len(h) > 4096 {
		return nil, false
	}
	for _, pair := range strings.Split(h, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(pair), " ")
		if k == "" || len(k) > 64 {
			return nil, false
		}
		dec, err := base64.StdEncoding.DecodeString(v)
		if err != nil {
			return nil, false
		}
		out[k] = string(dec)
	}
	return out, true
}

// TusCreate creates an upload for a planned item (POST /api/uploads).
func (s *Service) TusCreate(w http.ResponseWriter, r *http.Request, user string) {
	if !checkTusVersion(w, r) {
		return
	}
	if r.Header.Get("Upload-Defer-Length") != "" {
		tusError(w, http.StatusBadRequest, "deferred length not supported")
		return
	}
	length, err := strconv.ParseInt(r.Header.Get("Upload-Length"), 10, 64)
	if err != nil || length <= 0 {
		tusError(w, http.StatusBadRequest, "invalid Upload-Length")
		return
	}
	if length > s.cfg.MaxUploadSize {
		tusError(w, http.StatusRequestEntityTooLarge, "upload too large")
		return
	}
	meta, ok := parseMetadata(r.Header.Get("Upload-Metadata"))
	if !ok {
		tusError(w, http.StatusBadRequest, "invalid Upload-Metadata")
		return
	}
	if name := meta["filename"]; name != "" && naming.IsJunkName(name) {
		tusError(w, http.StatusBadRequest, "junk file rejected")
		return
	}
	ctx := r.Context()
	b, err := s.ownedBatch(ctx, user, meta["batch"])
	if err != nil {
		tusError(w, http.StatusNotFound, "batch not found")
		return
	}
	if b.Status != store.BatchActive {
		tusError(w, http.StatusConflict, "batch is not active")
		return
	}
	var it *store.Item
	for _, x := range b.Items {
		if x.ID == meta["item"] {
			it = x
		}
	}
	if it == nil {
		tusError(w, http.StatusNotFound, "item not found")
		return
	}
	switch it.Status {
	case store.ItemPending, store.ItemUploading, store.ItemFailed, store.ItemCancelled:
	default:
		tusError(w, http.StatusConflict, "item is "+it.Status)
		return
	}
	if length != it.Size {
		tusError(w, http.StatusBadRequest, "Upload-Length does not match the planned file size")
		return
	}
	if it.UploadID != "" { // a previous upload for this item is abandoned
		s.dropStaged(ctx, it.UploadID)
	}
	id := store.NewID()
	f, err := s.fs.CreateExclusive(s.stagingPath(id), 0o600)
	if err != nil {
		s.log.Error("create staging file", "err", err)
		tusError(w, http.StatusInternalServerError, "cannot create staging file")
		return
	}
	f.Close()
	if err := s.store.CreateUpload(ctx, &store.Upload{ID: id, ItemID: it.ID, BatchID: b.ID, User: user, Length: length}); err != nil {
		_ = s.fs.Remove(s.stagingPath(id))
		tusError(w, http.StatusInternalServerError, "database error")
		return
	}
	it.UploadID, it.Status, it.Error = id, store.ItemUploading, ""
	if err := s.store.UpdateItem(ctx, it); err != nil {
		tusError(w, http.StatusInternalServerError, "database error")
		return
	}
	tusHeaders(w)
	w.Header().Set("Location", UploadPath+id)
	w.WriteHeader(http.StatusCreated)
}

// ownedUpload loads an upload owned by user.
func (s *Service) ownedUpload(ctx context.Context, user, id string) (*store.Upload, error) {
	if !idRe.MatchString(id) {
		return nil, store.ErrNotFound
	}
	u, err := s.store.GetUpload(ctx, id)
	if err != nil {
		return nil, err
	}
	if u.User != user {
		return nil, store.ErrNotFound
	}
	return u, nil
}

func (s *Service) uploadOffset(u *store.Upload) (int64, error) {
	if !u.CompletedAt.IsZero() {
		return u.Length, nil
	}
	fi, err := s.fs.Lstat(s.stagingPath(u.ID))
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// TusHead reports the current offset (HEAD /api/uploads/{id}).
func (s *Service) TusHead(w http.ResponseWriter, r *http.Request, user, id string) {
	if !checkTusVersion(w, r) {
		return
	}
	u, err := s.ownedUpload(r.Context(), user, id)
	if err != nil {
		tusError(w, http.StatusNotFound, "")
		return
	}
	off, err := s.uploadOffset(u)
	if err != nil {
		tusError(w, http.StatusGone, "")
		return
	}
	tusHeaders(w)
	w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(u.Length, 10))
	w.WriteHeader(http.StatusOK)
}

// TusPatch appends data (PATCH /api/uploads/{id}). When the upload is
// complete the file is fsynced and published immediately.
func (s *Service) TusPatch(w http.ResponseWriter, r *http.Request, user, id string) {
	if !checkTusVersion(w, r) {
		return
	}
	if r.Header.Get("Content-Type") != "application/offset+octet-stream" {
		tusError(w, http.StatusUnsupportedMediaType, "Content-Type must be application/offset+octet-stream")
		return
	}
	ctx := r.Context()
	u, err := s.ownedUpload(ctx, user, id)
	if err != nil {
		tusError(w, http.StatusNotFound, "")
		return
	}
	rc := http.NewResponseController(w)
	release, ok := s.locks.acquire(id, func() { _ = rc.SetReadDeadline(time.Now()) })
	if !ok {
		tusError(w, http.StatusLocked, "upload is busy")
		return
	}
	defer release()

	if !u.CompletedAt.IsZero() {
		tusError(w, http.StatusConflict, "upload already complete")
		return
	}
	off, err := s.uploadOffset(u)
	if err != nil {
		tusError(w, http.StatusGone, "staging file missing")
		return
	}
	clientOff, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || clientOff != off {
		w.Header().Set("Upload-Offset", strconv.FormatInt(off, 10))
		tusError(w, http.StatusConflict, "offset mismatch")
		return
	}
	remaining := u.Length - off
	if cl := r.ContentLength; cl > remaining || cl > MaxChunk {
		tusError(w, http.StatusRequestEntityTooLarge, "chunk exceeds remaining length")
		return
	}

	f, err := s.fs.OpenWrite(s.stagingPath(id))
	if err != nil {
		tusError(w, http.StatusGone, "staging file missing")
		return
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		f.Close()
		tusError(w, http.StatusInternalServerError, "seek failed")
		return
	}
	written, copyErr := copyWithIdle(f, r.Body, min(remaining, MaxChunk), rc)
	newOff := off + written
	complete := newOff == u.Length
	if complete {
		if err := f.Sync(); err != nil {
			f.Close()
			tusError(w, http.StatusInternalServerError, "fsync failed")
			return
		}
	}
	if err := f.Close(); err != nil {
		tusError(w, http.StatusInternalServerError, "write failed")
		return
	}
	if copyErr != nil && !complete {
		// Interrupted (client gone, superseded by a newer PATCH, or stalled).
		// Bytes written so far are kept; the client resumes from HEAD.
		s.log.Debug("upload interrupted", "upload", id, "offset", newOff, "err", copyErr)
		if errors.Is(copyErr, errDiskWrite) {
			tusError(w, http.StatusInsufficientStorage, "write failed")
			return
		}
		tusError(w, http.StatusBadRequest, "upload interrupted")
		return
	}

	if complete {
		if err := s.complete(ctx, u); err != nil {
			s.log.Error("complete upload", "upload", id, "err", err)
			tusError(w, http.StatusInternalServerError, "publish failed")
			return
		}
	}
	tusHeaders(w)
	w.Header().Set("Upload-Offset", strconv.FormatInt(newOff, 10))
	w.WriteHeader(http.StatusNoContent)
}

var errDiskWrite = errors.New("disk write failed")

// copyWithIdle copies at most n bytes, extending the read deadline before
// each read so a stalled client cannot pin the handler forever.
func copyWithIdle(dst io.Writer, src io.Reader, n int64, rc *http.ResponseController) (int64, error) {
	buf := make([]byte, 1<<20)
	var total int64
	for total < n {
		_ = rc.SetReadDeadline(time.Now().Add(readIdle))
		want := int64(len(buf))
		if n-total < want {
			want = n - total
		}
		nr, rerr := src.Read(buf[:want])
		if nr > 0 {
			nw, werr := dst.Write(buf[:nr])
			total += int64(nw)
			if werr != nil {
				return total, errors.Join(errDiskWrite, werr)
			}
		}
		if rerr == io.EOF {
			return total, nil
		}
		if rerr != nil {
			return total, rerr
		}
	}
	return total, nil
}

// complete publishes the item belonging to a finished upload.
func (s *Service) complete(ctx context.Context, u *store.Upload) error {
	// Publishing must not be aborted half-way because the client went away.
	ctx = context.WithoutCancel(ctx)
	b, err := s.store.GetBatch(ctx, u.BatchID)
	if err != nil {
		return err
	}
	for _, it := range b.Items {
		if it.ID == u.ItemID {
			if it.UploadID != u.ID || it.Status != store.ItemUploading {
				return errors.New("item no longer expects this upload")
			}
			return s.publish(ctx, b, it)
		}
	}
	return store.ErrNotFound
}

// TusDelete terminates an upload (DELETE /api/uploads/{id}).
func (s *Service) TusDelete(w http.ResponseWriter, r *http.Request, user, id string) {
	if !checkTusVersion(w, r) {
		return
	}
	ctx := r.Context()
	u, err := s.ownedUpload(ctx, user, id)
	if err != nil {
		tusError(w, http.StatusNotFound, "")
		return
	}
	release, ok := s.locks.acquire(id, func() {})
	if !ok {
		tusError(w, http.StatusLocked, "upload is busy")
		return
	}
	defer release()
	if it, err := s.store.GetItem(ctx, u.ItemID); err == nil && it.UploadID == u.ID &&
		(it.Status == store.ItemUploading || it.Status == store.ItemConflict) {
		it.Status, it.UploadID = store.ItemCancelled, ""
		_ = s.store.UpdateItem(ctx, it)
		s.updateBatchStatus(ctx, it.BatchID)
	}
	if err := s.fs.Remove(s.stagingPath(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.log.Warn("remove staging file", "upload", id, "err", err)
	}
	_ = s.store.DeleteUpload(ctx, id)
	tusHeaders(w)
	w.WriteHeader(http.StatusNoContent)
}

// uploadLocks serializes access per upload. A new request interrupts the
// current holder (e.g. a PATCH stuck after the phone went to sleep) instead
// of waiting for its read timeout.
type uploadLocks struct {
	mu   sync.Mutex
	held map[string]*lockHolder
}

type lockHolder struct {
	done      chan struct{}
	interrupt func()
}

func (l *uploadLocks) acquire(id string, interrupt func()) (func(), bool) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		l.mu.Lock()
		if l.held == nil {
			l.held = map[string]*lockHolder{}
		}
		h, busy := l.held[id]
		if !busy {
			nh := &lockHolder{done: make(chan struct{}), interrupt: interrupt}
			l.held[id] = nh
			l.mu.Unlock()
			return func() {
				l.mu.Lock()
				delete(l.held, id)
				l.mu.Unlock()
				close(nh.done)
			}, true
		}
		l.mu.Unlock()
		h.interrupt()
		wait := time.Until(deadline)
		if wait <= 0 {
			return nil, false
		}
		select {
		case <-h.done:
		case <-time.After(wait):
			return nil, false
		}
	}
}
