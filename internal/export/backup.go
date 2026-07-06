package export

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/garywhat/devinmonitor/internal/model"
)

// BackupDocument is the normalized JSON backup container. It wraps the
// existing export Document with backup-specific metadata.
type BackupDocument struct {
	BackupType   string    `json:"backup_type"` // "json" | "db"
	GeneratedAt  time.Time `json:"generated_at"`
	SourceDBPath string    `json:"source_db_path,omitempty"`
	Document     Document  `json:"document"`
}

// WriteBackupJSON writes a normalized JSON backup of all sessions (with
// per-request detail) to w.
func WriteBackupJSON(w io.Writer, ss []model.Session, sourceDBPath string) error {
	doc := BuildDocument(ss, true)
	backup := BackupDocument{
		BackupType:   "json",
		GeneratedAt:  time.Now(),
		SourceDBPath: sourceDBPath,
		Document:     doc,
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(backup)
}

// CopyDB copies the sessions database file to dstPath. This is a plain byte
// copy of the SQLite file. Returns an error if srcPath does not exist.
func CopyDB(srcPath, dstPath string) error {
	if srcPath == "" {
		return fmt.Errorf("source db path is empty")
	}
	in, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source db: %w", err)
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return err
	}
	out, err := os.Create(dstPath)
	if err != nil {
		return fmt.Errorf("create dest db: %w", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return fmt.Errorf("copy db: %w", err)
	}
	return out.Sync()
}
