package main

// Compress (#199). dufs can rename, move and copy, but it cannot make a zip on the PC, so
// the gateway does that one thing itself. It answers on the folder's own path, so it is
// reached the same way as the rest of the folder and the server's check of which folders
// a person may open applies to it unchanged.

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"
)

// compressQuery marks the request. dufs gives it no meaning, so nothing of dufs's is shadowed.
const compressQuery = "compress"

// compress answers `POST /{app}/{dir}/?compress` with {"name": "beach.zip", "entries":
// ["beach.jpg", "2026"]}. The entries are names in dir, and the zip is written next to them.
func compress(w http.ResponseWriter, r *http.Request, a app, log *slog.Logger) {
	var in struct {
		Name    string   `json:"name"`
		Entries []string `json:"entries"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}
	if !plainName(in.Name) || !strings.HasSuffix(strings.ToLower(in.Name), ".zip") || len(in.Entries) == 0 ||
		slices.ContainsFunc(in.Entries, func(e string) bool { return !plainName(e) }) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a name ending in .zip and at least one entry, each a name in this folder"})
		return
	}
	root := sharedPath(a)
	if root == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	dir := strings.Trim(strings.TrimPrefix(r.URL.Path, "/"+a.Name), "/")
	if status := uploadAllowed(r, a, dir); status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}

	// os.Root refuses any path that leaves the shared folder, a symlink pointing out
	// included, so nothing below has to check a path by hand.
	shared, err := os.OpenRoot(root)
	if err != nil {
		log.Error("compress: the shared folder cannot be opened", "address", a.Address, "err", pathless(err),
			"error_code", "compress_open_failed", "step", "compress")
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer shared.Close()
	if dir == "" {
		dir = "."
	}
	d, err := shared.OpenRoot(dir)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer d.Close()
	if _, err := d.Lstat(in.Name); err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": in.Name + " is taken"})
		return
	}

	// Written under a hidden name and moved into place once whole, so a half-written zip
	// never appears under the name the person chose.
	var b [8]byte
	_, _ = rand.Read(b[:])
	tmp := "." + in.Name + "." + hex.EncodeToString(b[:]) + ".tmp"
	err = writeZip(d, tmp, in.Entries)
	if err == nil {
		err = place(d, tmp, in.Name)
	}
	_ = d.Remove(tmp)
	switch {
	case errors.Is(err, fs.ErrExist):
		writeJSON(w, http.StatusConflict, map[string]string{"error": in.Name + " is taken"})
	case errors.Is(err, fs.ErrNotExist):
		http.Error(w, "not found", http.StatusNotFound)
	case err != nil:
		// A symlink out of the folder lands here, as does a full disk.
		log.Warn("compress failed", "address", a.Address, "err", pathless(err),
			"error_code", "compress_failed", "step", "compress")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "these entries cannot be compressed"})
	default:
		writeJSON(w, http.StatusCreated, map[string]string{"name": in.Name})
	}
}

// uploadAllowed asks dufs whether this caller may upload to dir, with the caller's own
// credentials, so the answer is the one a PUT to the same folder would get.
func uploadAllowed(r *http.Request, a app, dir string) int {
	u := url.URL{Scheme: "http", Host: a.Address, Path: "/" + a.Name + "/" + dir, RawQuery: "json"}
	if dir != "" {
		u.Path += "/"
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, u.String(), nil)
	if err != nil {
		return http.StatusBadRequest
	}
	for _, h := range []string{"Authorization", "Cookie"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return http.StatusBadGateway
	}
	defer resp.Body.Close()
	var index struct {
		AllowUpload bool `json:"allow_upload"`
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return http.StatusNotFound
	case resp.StatusCode != http.StatusOK, json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&index) != nil,
		!index.AllowUpload:
		return http.StatusForbidden
	}
	return http.StatusOK
}

func writeZip(d *os.Root, name string, entries []string) error {
	f, err := d.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		if err = addEntry(zw, d.FS(), e); err != nil {
			break
		}
	}
	if cerr := zw.Close(); err == nil {
		err = cerr
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// addEntry adds a file, or a folder and everything under it. A symlink is followed while
// it stays inside the shared folder; one that points out fails the whole zip.
func addEntry(zw *zip.Writer, fsys fs.FS, entry string) error {
	return fs.WalkDir(fsys, entry, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := fs.Stat(fsys, p)
		if err != nil {
			return err
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		h.Name = p
		if info.IsDir() {
			h.Name += "/"
			_, err = zw.CreateHeader(h)
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		h.Method = zip.Deflate
		out, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		in, err := fsys.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(out, in)
		return err
	})
}

// place moves the finished zip to its name without replacing anything. A hard link fails
// on a name that is taken, where a rename would overwrite it. A disk with no hard links
// (FAT) gets a rename after a last look.
func place(d *os.Root, tmp, name string) error {
	err := d.Link(tmp, name)
	if err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	if _, err := d.Lstat(name); err == nil {
		return fs.ErrExist
	}
	// ponytail: a file made under this name between the look and the rename is replaced.
	// It needs two writers racing for one name on a disk without hard links.
	return d.Rename(tmp, name)
}

// pathless keeps what went wrong and drops the path an os error carries, which names a
// file or folder in the share (ADR 0007).
func pathless(err error) string {
	var pe *fs.PathError
	if errors.As(err, &pe) {
		return pe.Op + ": " + pe.Err.Error()
	}
	var le *os.LinkError
	if errors.As(err, &le) {
		return le.Op + ": " + le.Err.Error()
	}
	return err.Error()
}

func plainName(s string) bool {
	return s != "" && s != "." && s != ".." && path.Base(s) == s && !strings.ContainsAny(s, `/\`)
}
