package handler

import (
	"context"
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
	"github.com/viccom/cfgsync/internal/semver"
)

// Canonical document filenames inside the package root. Spec §4.1.
const (
	docReadme           = "README.md"
	docInstall          = "INSTALL.md"
	docUsage            = "USAGE.md"
	docChangelog        = "CHANGELOG.md"
	assetIcon           = "icon.png"
	assetScreenshotsDir = "screenshots"
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
		// uploadID is unique per HTTP request so concurrent uploads to the
		// same app_id do not collide in the staging tree. Each Stage call
		// wipes its own _staging/{app_id}/{uploadID}/ subtree, never the
		// sibling directories of in-flight concurrent uploads.
		uploadID := auth.NewID()
		stage, err := repository.Stage(appID, uploadID, part, cfg.MaxPackageBytes)
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

		// 4-7. Validate manifest, enforce README, collect docs + assets.
		// Each failure path inside parseStagedPackage writes the matching
		// HTTP response (400 invalid_manifest / readme_required /
		// version_mismatch / doc_too_large / etc.) and Discards the staging
		// dir so processUpload only has to handle the success path.
		parsed, ok := parseStagedPackage(w, repository, stage.StagingDir, files, cfg, urlVersion)
		if !ok {
			return
		}
		mf := parsed.mf
		ver := parsed.ver

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

		// 9. Marshal caches for DB storage. Marshal errors would mean a
		// programming bug (collectDocs/collectAssets return well-typed
		// maps, manifest is struct-only). Surface as 500 so we never
		// silently write "{}" to manifest_json.
		manifestJSON, err := json.Marshal(mf)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "marshal_manifest", err)
			return
		}
		docsJSON, err := json.Marshal(parsed.docs)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "marshal_docs", err)
			return
		}
		assetsJSON, err := json.Marshal(parsed.assets)
		if err != nil {
			repository.Discard(stage.StagingDir)
			writeInternal(w, "marshal_assets", err)
			return
		}

		// 10. DB tx: delete existing row (overwrite) → insert new release →
		//     sync app_tags → bump apps cache fields.
		releaseNotes := parsed.docs[docChangelog]
		iconPath := ""
		for _, a := range parsed.assets {
			if a["kind"] == "icon" {
				// Type assertion with ok-check — the asset map is built by
				// collectAssets one line above, so path is always a string
				// today, but a future asset kind that uses a non-string
				// path would panic here without the guard.
				if p, ok := a["path"].(string); ok {
					iconPath = p
				}
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
			string(parsed.manifestBytes), string(manifestJSON),
			stage.Size, stage.SHA256Hex,
			string(docsJSON), string(assetsJSON),
			releaseNotes, now, uid,
		)
		if err != nil {
			repository.Discard(stage.StagingDir)
			// POST concurrency: two clients saw existingID=0 (the SELECT
			// raced with the other tx), both INSERT, second one hits the
			// UNIQUE(app_id, version) constraint. Surface as 409 so the
			// caller can retry with PUT to overwrite, matching the
			// existingID>0 path above.
			if isUniqueViolation(err) {
				writeJSON(w, http.StatusConflict, map[string]interface{}{
					"error":   "version_exists",
					"version": mf.Version,
				})
				return
			}
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
				display_name = ?, summary = ?, description = COALESCE(NULLIF(?, ''), description),
				icon_path = ?, latest_version = ?, visibility = ?, updated_at = ?
			  WHERE app_id = ?`,
			mf.DisplayName, summary, mf.Description, iconPath, mf.Version, visibility, now, appID,
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

		// 11. Promote staging → final. FS operations can't be enrolled in
		// the SQL tx, so a failure here leaves the DB row committed but
		// the package absent on disk. Compensate via compensatePromoteFailure.
		if _, err := repository.Promote(stage.StagingDir, appID, mf.Version); err != nil {
			repository.Discard(stage.StagingDir)
			log.Printf("dev.promote_failed %s/%s after DB commit: %v — compensating", appID, mf.Version, err)
			compensatePromoteFailure(db, appID, mf.Version)
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
			"docs":           docNames(parsed.docs),
			"assets":         assetPaths(parsed.assets),
			"created_at":     now,
		})
	}
}

// ListDevReleases returns the full release history for one app, newest first.
// Admin view — includes sha256 and size. Ordering happens in Go via
// semver.Parsed.Compare because SQLite's text ordering of version_pre does
// not match semver §11 (e.g. "rc.11" < "rc.2" lexically, but rc.11 > rc.2
// semantically). The schema still stores version_major/minor/patch/pre for
// potential indexing use, but they are not trusted for ordering.
func ListDevReleases(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		appID := r.PathValue("app_id")
		rows, err := db.QueryContext(r.Context(),
			`SELECT version, package_size, package_sha256, manifest_json,
			       release_notes, created_at, created_by
			  FROM app_releases
			 WHERE app_id = ?`,
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
		sortReleasesBySemverDesc(out, func(i int) string { return out[i].Version })
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

		// Recompute apps.latest_version. Ordering is done in Go because
		// SQLite's text ordering of version_pre does not match semver §11
		// (see sortReleasesBySemverDesc doc).
		versionRows, err := tx.QueryContext(r.Context(),
			`SELECT version FROM app_releases WHERE app_id = ?`,
			appID,
		)
		if err != nil {
			writeInternal(w, "delete_recompute_latest", err)
			return
		}
		var versions []string
		for versionRows.Next() {
			var v string
			if err := versionRows.Scan(&v); err != nil {
				versionRows.Close()
				writeInternal(w, "delete_recompute_scan", err)
				return
			}
			versions = append(versions, v)
		}
		versionRows.Close()
		if err := versionRows.Err(); err != nil {
			writeInternal(w, "delete_recompute_rows", err)
			return
		}
		newLatest := pickLatestVersion(versions)
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

// compensatePromoteFailure reverts the DB-side effects of processUpload when
// the SQL tx has committed but the subsequent Promote (FS rename) failed: it
// deletes the release row and recomputes apps.latest_version from surviving
// releases.
//
// Three deliberate choices vs the previous inline compensation:
//
//  1. Independent context. The request context is often already cancelled by
//     the time Promote fails (I/O timeouts frequently follow client disconnect),
//     which would make ExecContext return context.Canceled and leave the
//     orphaned row in place.
//  2. Atomic. DELETE + UPDATE run inside one tx so partial failure cannot
//     leave apps.latest_version pointing at the deleted row.
//  3. pickLatestVersion(remaining) instead of restoring a captured prev value.
//     In the PUT-overwrite case where version X was latest and is being
//     replaced, prev==X but X is now deleted — restoring prev would re-point
//     latest at a missing row. Recomputing from the remaining releases always
//     yields a value that actually exists (or "" if no releases remain).
//
// Best-effort: each failure mode is logged so the operator can spot
// inconsistent state in the access log. The caller still reports 500 to the
// client regardless of whether compensation succeeded.
func compensatePromoteFailure(db *sql.DB, appID, version string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Printf("dev.compensate_begin_failed %s/%s: %v", appID, version, err)
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM app_releases WHERE app_id = ? AND version = ?`,
		appID, version,
	); err != nil {
		log.Printf("dev.compensate_delete_failed %s/%s: %v", appID, version, err)
		return
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT version FROM app_releases WHERE app_id = ?`,
		appID,
	)
	if err != nil {
		log.Printf("dev.compensate_recompute_query_failed %s: %v", appID, err)
		return
	}
	var versions []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			log.Printf("dev.compensate_recompute_scan_failed %s: %v", appID, err)
			return
		}
		versions = append(versions, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Printf("dev.compensate_recompute_rows_failed %s: %v", appID, err)
		return
	}
	newLatest := pickLatestVersion(versions)
	if _, err := tx.ExecContext(ctx,
		`UPDATE apps SET latest_version = ?, updated_at = ? WHERE app_id = ?`,
		newLatest, time.Now().Unix(), appID,
	); err != nil {
		log.Printf("dev.compensate_apps_revert_failed %s: %v", appID, err)
		return
	}
	if err := tx.Commit(); err != nil {
		log.Printf("dev.compensate_commit_failed %s: %v", appID, err)
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

// parsedPackage bundles everything processUpload needs after manifest +
// docs + assets validation has passed. Built by parseStagedPackage so
// processUpload can stay linear and not re-do validation inlined.
type parsedPackage struct {
	manifestBytes []byte
	mf            manifest.Manifest
	ver           semver.Parsed
	docs          map[string]string
	assets        []map[string]interface{}
}

// parseStagedPackage runs the validation steps that depend only on the
// extracted staging dir (steps 4-7 of the original processUpload): manifest
// presence/size/parse, URL version match for PUT, README presence, and
// docs/assets collection with per-file caps.
//
// On any failure it writes the appropriate 400 response, Discards the
// staging dir, and returns (zero, false) so the caller just returns. On
// success returns (parsed, true); the caller owns the staging dir for the
// remaining DB/FS dance.
func parseStagedPackage(
	w http.ResponseWriter,
	repository *repo.Repo,
	stagingDir string,
	files []string,
	cfg *config.Config,
	urlVersion string,
) (parsedPackage, bool) {
	if !containsFile(files, "manifest.yaml") {
		repository.Discard(stagingDir)
		writeError(w, http.StatusBadRequest, "manifest_required")
		return parsedPackage{}, false
	}
	manifestBytes, err := readCappedFile(filepath.Join(stagingDir, "extracted", "manifest.yaml"), cfg.MaxManifestBytes)
	if err != nil {
		repository.Discard(stagingDir)
		writeError(w, http.StatusBadRequest, "manifest_too_large")
		return parsedPackage{}, false
	}
	mf, ver, err := manifest.ParseAndValidate(manifestBytes)
	if err != nil {
		repository.Discard(stagingDir)
		var ve *manifest.ValidationError
		if errors.As(err, &ve) {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"error":  "invalid_manifest",
				"fields": ve.Fields,
			})
			return parsedPackage{}, false
		}
		writeError(w, http.StatusBadRequest, "invalid_manifest")
		return parsedPackage{}, false
	}
	if urlVersion != "" && urlVersion != mf.Version {
		repository.Discard(stagingDir)
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error":    "version_mismatch",
			"url":      urlVersion,
			"manifest": mf.Version,
		})
		return parsedPackage{}, false
	}
	if !containsFile(files, docReadme) {
		repository.Discard(stagingDir)
		writeError(w, http.StatusBadRequest, "readme_required")
		return parsedPackage{}, false
	}
	docs, err := collectDocs(stagingDir, files, cfg)
	if err != nil {
		repository.Discard(stagingDir)
		writeError(w, http.StatusBadRequest, err.Error())
		return parsedPackage{}, false
	}
	assets, err := collectAssets(stagingDir, files, cfg)
	if err != nil {
		repository.Discard(stagingDir)
		writeError(w, http.StatusBadRequest, err.Error())
		return parsedPackage{}, false
	}
	return parsedPackage{
		manifestBytes: manifestBytes,
		mf:            mf,
		ver:           ver,
		docs:          docs,
		assets:        assets,
	}, true
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
