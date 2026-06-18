package handler

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/viccom/cfgsync/internal/auth"
	"github.com/viccom/cfgsync/internal/config"
	"github.com/viccom/cfgsync/internal/manifest"
	"github.com/viccom/cfgsync/internal/repo"
)

// Canonical document filenames inside the package root. Spec §4.1.
const (
	docReadme            = "README.md"
	docInstall           = "INSTALL.md"
	docUsage             = "USAGE.md"
	docChangelog         = "CHANGELOG.md"
	assetIcon            = "icon.png"
	assetScreenshotsDir  = "screenshots"
)

// UploadRelease handles POST /api/v1/dev/apps/{app_id}/releases. Reads a
// multipart "package" field (.tar.gz), validates the manifest inside, and
// commits the release. Returns 409 if (app_id, version) already exists —
// use PUT to overwrite.
func UploadRelease(db *sql.DB, cfg *config.Config, r *repo.Repo) http.HandlerFunc {
	return processUpload(db, cfg, r, false)
}

// OverwriteRelease handles PUT /api/v1/dev/apps/{app_id}/releases/{version}.
// Same as UploadRelease but replaces the existing release and requires the
// manifest version field to match {version}.
func OverwriteRelease(db *sql.DB, cfg *config.Config, r *repo.Repo) http.HandlerFunc {
	return processUpload(db, cfg, r, true)
}

func processUpload(db *sql.DB, cfg *config.Config, repository *repo.Repo, allowOverwrite bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		uid := auth.UserID(r.Context())
		appID := r.PathValue("app_id")
		urlVersion := r.PathValue("version") // only set on PUT

		// 1. App must exist.
		var exists int
		err := db.QueryRowContext(r.Context(),
			`SELECT 1 FROM apps WHERE app_id = ?`, appID,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		if err != nil {
			writeInternal(w, "lookup_app", err)
			return
		}

		// 2. Stream multipart body. Stage enforces the package size cap
		// internally via LimitReader; we don't add a separate MaxBytesReader
		// because multipart overhead (boundary, headers) is small and the
		// Stage check is the load-bearing one.
		mr, err := r.MultipartReader()
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_multipart")
			return
		}
		part, err := findPackagePart(mr)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		stage, err := repository.Stage(appID, "_pending", part, cfg.MaxPackageBytes)
		if err != nil {
			if errors.Is(err, repo.ErrPackageTooLarge) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]interface{}{
					"error":     "package_too_large",
					"max_bytes": cfg.MaxPackageBytes,
				})
				return
			}
			writeInternal(w, "stage", err)
			return
		}

		// 3. Extract tar.gz into staging/extracted/.
		files, err := repository.Extract(stage.StagingDir)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeError(w, http.StatusBadRequest, "invalid_package")
			return
		}

		// 4. Read + parse manifest.yaml.
		if !containsFile(files, "manifest.yaml") {
			repository.Discard(stage.StagingDir)
			writeError(w, http.StatusBadRequest, "manifest_required")
			return
		}
		manifestBytes, err := readCappedFile(filepath.Join(stage.StagingDir, "extracted", "manifest.yaml"), cfg.MaxManifestBytes)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeError(w, http.StatusBadRequest, "manifest_too_large")
			return
		}
		mf, ver, err := manifest.ParseAndValidate(manifestBytes)
		if err != nil {
			repository.Discard(stage.StagingDir)
			var ve *manifest.ValidationError
			if errors.As(err, &ve) {
				writeJSON(w, http.StatusBadRequest, map[string]interface{}{
					"error":  "invalid_manifest",
					"fields": ve.Fields,
				})
				return
			}
			writeError(w, http.StatusBadRequest, "invalid_manifest")
			return
		}

		// 5. URL version (PUT) must match manifest version.
		if urlVersion != "" && urlVersion != mf.Version {
			repository.Discard(stage.StagingDir)
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":    "version_mismatch",
				"url":      urlVersion,
				"manifest": mf.Version,
			})
			return
		}

		// 6. README.md required.
		if !containsFile(files, docReadme) {
			repository.Discard(stage.StagingDir)
			writeError(w, http.StatusBadRequest, "readme_required")
			return
		}

		// 7. Collect docs and assets (each capped per spec §4.2).
		docs, err := collectDocs(stage.StagingDir, files, cfg)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		assets, err := collectAssets(stage.StagingDir, files, cfg)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}

		// 8. Existing release check.
		var existingID int64
		_ = db.QueryRowContext(r.Context(),
			`SELECT id FROM app_releases WHERE app_id = ? AND version = ?`,
			appID, mf.Version,
		).Scan(&existingID)
		if existingID != 0 && !allowOverwrite {
			repository.Discard(stage.StagingDir)
			writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error":   "version_exists",
				"version": mf.Version,
			})
			return
		}

		// 9. Marshal caches for DB storage.
		manifestJSON, _ := json.Marshal(mf)
		docsJSON, _ := json.Marshal(docs)
		assetsJSON, _ := json.Marshal(assets)

		// 10. DB tx: delete existing row (overwrite) → insert new release →
		//     sync app_tags → bump apps cache fields.
		releaseNotes := docs[docChangelog]
		iconPath := ""
		for _, a := range assets {
			if a["kind"] == "icon" {
				iconPath = a["path"].(string)
				break
			}
		}
		summary := mf.Summary
		if summary == "" {
			summary = mf.Description
		}
		visibility := mf.Visibility
		if visibility == "" {
			visibility = "public"
		}
		now := time.Now().Unix()

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "begin_tx", err)
			return
		}
		defer tx.Rollback()

		if existingID != 0 {
			if _, err := tx.ExecContext(r.Context(),
				`DELETE FROM app_releases WHERE id = ?`, existingID,
			); err != nil {
				repository.Discard(stage.StagingDir)
				writeInternal(w, "delete_existing", err)
				return
			}
		}
		_, err = tx.ExecContext(r.Context(),
			`INSERT INTO app_releases (
				app_id, version, version_major, version_minor, version_patch, version_pre,
				manifest_yaml, manifest_json, package_size, package_sha256,
				docs_json, assets_json, release_notes, created_at, created_by
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			appID, mf.Version, ver.Major, ver.Minor, ver.Patch, ver.Pre,
			string(manifestBytes), string(manifestJSON),
			stage.Size, stage.SHA256Hex,
			string(docsJSON), string(assetsJSON),
			releaseNotes, now, uid,
		)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "insert_release", err)
			return
		}
		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM app_tags WHERE app_id = ?`, appID,
		); err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "delete_tags", err)
			return
		}
		for _, tag := range mf.Tags {
			if _, err := tx.ExecContext(r.Context(),
				`INSERT OR IGNORE INTO app_tags (app_id, tag) VALUES (?, ?)`,
				appID, tag,
			); err != nil {
				repository.Discard(stage.StagingDir)
				writeInternal(w, "insert_tag", err)
				return
			}
		}
		// description: keep existing unless manifest supplies a non-empty value.
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE apps SET
				summary = ?, description = COALESCE(NULLIF(?, ''), description),
				icon_path = ?, latest_version = ?, visibility = ?, updated_at = ?
			  WHERE app_id = ?`,
			summary, mf.Description, iconPath, mf.Version, visibility, now, appID,
		); err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "update_apps", err)
			return
		}
		if err := tx.Commit(); err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "commit", err)
			return
		}

		// 11. Promote staging → final. If this fails the DB row exists but
		// the package is absent — caller recovers by re-PUT.
		if _, err := repository.Promote(stage.StagingDir, appID, mf.Version); err != nil {
			log.Printf("dev.promote PANIC %s/%s failed AFTER DB commit: %v", appID, mf.Version, err)
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}

		// 12. Response.
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"app_id":         appID,
			"version":        mf.Version,
			"package_size":   stage.Size,
			"package_sha256": stage.SHA256Hex,
			"manifest":       mf,
			"docs":           docNames(docs),
			"assets":         assetPaths(assets),
			"created_at":     now,
		})
	}
}

// ListDevReleases returns the full release history for one app, newest first.
// Admin view — includes sha256 and size.
func ListDevReleases(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		rows, err := db.QueryContext(r.Context(),
			`SELECT version, package_size, package_sha256, manifest_json,
			       release_notes, created_at, created_by
			  FROM app_releases
			 WHERE app_id = ?
			 ORDER BY version_major DESC, version_minor DESC,
			          version_patch DESC, version_pre ASC`,
			appID,
		)
		if err != nil {
			writeInternal(w, "list_query", err)
			return
		}
		defer rows.Close()
		type rel struct {
			Version       string                 `json:"version"`
			PackageSize   int64                  `json:"package_size"`
			PackageSHA256 string                 `json:"package_sha256"`
			Manifest      map[string]interface{} `json:"manifest,omitempty"`
			ReleaseNotes  string                 `json:"release_notes,omitempty"`
			CreatedAt     int64                  `json:"created_at"`
			CreatedBy     string                 `json:"created_by"`
		}
		out := make([]rel, 0)
		for rows.Next() {
			var item rel
			var manifestJSON string
			var notes sql.NullString
			if err := rows.Scan(
				&item.Version, &item.PackageSize, &item.PackageSHA256,
				&manifestJSON, &notes,
				&item.CreatedAt, &item.CreatedBy,
			); err != nil {
				writeInternal(w, "list_scan", err)
				return
			}
			if notes.Valid {
				item.ReleaseNotes = notes.String
			}
			_ = json.Unmarshal([]byte(manifestJSON), &item.Manifest)
			out = append(out, item)
		}
		if err := rows.Err(); err != nil {
			writeInternal(w, "list_rows_err", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"app_id":   appID,
			"releases": out,
		})
	}
}

// DeleteRelease removes one version: DB row first, then the FS directory.
// Idempotent on FS — a missing directory is not an error.
func DeleteRelease(db *sql.DB, repository *repo.Repo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		version := r.PathValue("version")

		tx, err := db.BeginTx(r.Context(), nil)
		if err != nil {
			writeInternal(w, "delete_begin", err)
			return
		}
		defer tx.Rollback()

		res, err := tx.ExecContext(r.Context(),
			`DELETE FROM app_releases WHERE app_id = ? AND version = ?`,
			appID, version,
		)
		if err != nil {
			writeInternal(w, "delete_release_row", err)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}

		// Recompute apps.latest_version.
		var latest sql.NullString
		if err := tx.QueryRowContext(r.Context(),
			`SELECT version FROM app_releases
			  WHERE app_id = ?
			  ORDER BY version_major DESC, version_minor DESC, version_patch DESC, version_pre ASC
			  LIMIT 1`,
			appID,
		).Scan(&latest); err != nil && !errors.Is(err, sql.ErrNoRows) {
			writeInternal(w, "delete_recompute_latest", err)
			return
		}
		newLatest := ""
		if latest.Valid {
			newLatest = latest.String
		}
		if _, err := tx.ExecContext(r.Context(),
			`UPDATE apps SET latest_version = ?, updated_at = ? WHERE app_id = ?`,
			newLatest, time.Now().Unix(), appID,
		); err != nil {
			writeInternal(w, "delete_update_apps", err)
			return
		}
		if err := tx.Commit(); err != nil {
			writeInternal(w, "delete_commit", err)
			return
		}

		if err := repository.DeleteRelease(appID, version); err != nil {
			log.Printf("dev.delete_fs %s/%s orphaned: %v", appID, version, err)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// --- helpers ---

// writeInternal logs the underlying error and emits the standard 500
// response. Centralised so we can grep for "dev.<step>" when chasing 500s.
func writeInternal(w http.ResponseWriter, where string, err error) {
	log.Printf("dev.%s internal: %v", where, err)
	writeError(w, http.StatusInternalServerError, "internal")
}

func findPackagePart(mr *multipart.Reader) (*multipart.Part, error) {
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			return nil, errors.New("missing_package")
		}
		if err != nil {
			return nil, errors.New("invalid_multipart")
		}
		if p.FormName() == "package" {
			return p, nil
		}
	}
}

func containsFile(files []string, name string) bool {
	for _, f := range files {
		if f == name {
			return true
		}
	}
	return false
}

func readCappedFile(path string, max int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, int64(max)+1))
}

func collectDocs(stagingDir string, files []string, cfg *config.Config) (map[string]string, error) {
	out := map[string]string{}
	for _, name := range []string{docReadme, docInstall, docUsage, docChangelog} {
		if !containsFile(files, name) {
			continue
		}
		b, err := readCappedFile(filepath.Join(stagingDir, "extracted", name), cfg.MaxDocBytes)
		if err != nil {
			return nil, errors.New("doc_read_failed")
		}
		if len(b) > cfg.MaxDocBytes {
			return nil, errors.New("doc_too_large")
		}
		out[name] = string(b)
	}
	return out, nil
}

func collectAssets(stagingDir string, files []string, cfg *config.Config) ([]map[string]interface{}, error) {
	out := make([]map[string]interface{}, 0)

	if containsFile(files, assetIcon) {
		b, err := readCappedFile(filepath.Join(stagingDir, "extracted", assetIcon), cfg.MaxIconBytes)
		if err != nil {
			return nil, errors.New("icon_read_failed")
		}
		if len(b) > cfg.MaxIconBytes {
			return nil, errors.New("icon_too_large")
		}
		out = append(out, map[string]interface{}{
			"kind": "icon",
			"path": assetIcon,
			"size": len(b),
		})
	}

	var shots []string
	for _, f := range files {
		if strings.HasPrefix(f, assetScreenshotsDir+"/") {
			shots = append(shots, f)
		}
	}
	sort.Strings(shots)
	if len(shots) > cfg.MaxScreenshots {
		return nil, errors.New("too_many_screenshots")
	}
	for _, p := range shots {
		b, err := readCappedFile(filepath.Join(stagingDir, "extracted", p), cfg.MaxScreenshotBytes)
		if err != nil {
			return nil, errors.New("screenshot_read_failed")
		}
		if len(b) > cfg.MaxScreenshotBytes {
			return nil, errors.New("screenshot_too_large")
		}
		out = append(out, map[string]interface{}{
			"kind": "screenshot",
			"path": p,
			"size": len(b),
		})
	}
	return out, nil
}

func docNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assetPaths(assets []map[string]interface{}) []string {
	out := make([]string, 0, len(assets))
	for _, a := range assets {
		if p, ok := a["path"].(string); ok {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}
