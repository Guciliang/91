package main

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"
	"time"

	"github.com/video-site/backend/internal/catalog"
	"github.com/video-site/backend/internal/config"
	"github.com/video-site/backend/internal/drives"
	"github.com/video-site/backend/internal/proxy"
	"github.com/video-site/backend/internal/scanjob"
)

func scanResultTestApp(t *testing.T) (*App, *serverTreeScanDrive) {
	t.Helper()
	cat, err := catalog.Open(filepath.Join(t.TempDir(), "catalog.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cat.Close() })
	const id = "scan-result-drive"
	if err := cat.UpsertDrive(context.Background(), &catalog.Drive{ID: id, Kind: "fake", Name: "Scan test", RootID: "root"}); err != nil {
		t.Fatal(err)
	}
	drv := &serverTreeScanDrive{id: id, entries: map[string][]drives.Entry{"root": {}}}
	registry := proxy.NewRegistry()
	registry.Set(id, drv)
	return &App{cat: cat, registry: registry, cfg: &config.Config{
		Scanner: config.Scanner{VideoExtensions: []string{".mp4"}},
		Storage: config.Storage{LocalPreviewDir: t.TempDir()},
	}}, drv
}

func TestScanReplacementSurvivesFirstScanAndRetainsLiveDeduplication(t *testing.T) {
	for _, hash := range []string{"same-hash", ""} {
		t.Run("hash="+hash, func(t *testing.T) {
			app, drv := scanResultTestApp(t)
			ctx := context.Background()
			now := time.Now()
			oldName := "clip.mp4"
			if hash != "" {
				oldName = "original.mp4"
			}
			if err := app.cat.UpsertVideo(ctx, &catalog.Video{
				ID: "old-row", DriveID: drv.ID(), FileID: "old-file", FileName: oldName,
				ContentHash: hash, Size: 123, ParentID: "root", AncestorDirIDs: []string{"root"},
				Title: "Clip", CreatedAt: now, PublishedAt: now, UpdatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			drv.entries["root"] = []drives.Entry{
				{ID: "replacement", Name: "clip.mp4", Hash: hash, Size: 123},
				{ID: "another-copy", Name: "clip.mp4", Hash: hash, Size: 123},
			}
			result := app.runScan(ctx, drv.ID())
			if result.State != scanjob.Succeeded || result.AddedCount != 1 || result.DuplicateCount != 1 {
				t.Fatalf("unexpected result: %+v", result)
			}
			items, err := app.cat.ListVideosByDrive(ctx, drv.ID())
			if err != nil {
				t.Fatal(err)
			}
			if len(items) != 1 || items[0].FileID != "replacement" {
				t.Fatalf("first scan must retain the live replacement: %+v", items)
			}
			again := app.runScan(ctx, drv.ID())
			if again.AddedCount != 0 || again.State != scanjob.Succeeded {
				t.Fatalf("rescan = %+v", again)
			}
		})
	}
}

func TestScanReportsAndPersistsPartialDiscovery(t *testing.T) {
	app, drv := scanResultTestApp(t)
	drv.entries["root"] = []drives.Entry{
		{ID: "failed-dir", Name: "Unavailable", IsDir: true},
		{ID: "live-file", Name: "clip.mp4", Size: 123},
	}
	drv.listErrors = map[string]error{"failed-dir": errors.New("provider unavailable")}
	result := app.runScan(context.Background(), drv.ID())
	if result.State != scanjob.Partial || result.ErrorCount != 1 || result.ScannedCount != 1 || result.AddedCount != 1 {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Issues) != 1 || result.Issues[0].Stage != "discovery" {
		t.Fatalf("issues = %+v", result.Issues)
	}
	stored, err := app.cat.LatestScanResults(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored[drv.ID()].State != scanjob.Partial || stored[drv.ID()].AddedCount != 1 {
		t.Fatalf("stored = %+v", stored)
	}
	if app.driveHasActiveWork(drv.ID()) {
		t.Fatal("finished results must not keep the drive busy")
	}
}

type finalPassScanDrive struct {
	*serverTreeScanDrive
	failures int
	calls    int
}

func (d *finalPassScanDrive) List(ctx context.Context, dirID string) ([]drives.Entry, error) {
	if dirID == "retry-dir" {
		d.calls++
		if d.calls <= d.failures {
			return nil, io.ErrUnexpectedEOF
		}
	}
	return d.serverTreeScanDrive.List(ctx, dirID)
}

func TestScanPersistsFinalRetryOutcomeAndProtectsUnreadableFiles(t *testing.T) {
	for _, tt := range []struct {
		name       string
		failures   int
		calls      int
		state      scanjob.State
		errors     int
		scanned    int
		firstAdded int
	}{
		{"recovered", 3, 4, scanjob.Succeeded, 0, 3, 2},
		{"still unavailable", 6, 6, scanjob.Partial, 1, 1, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			app, base := scanResultTestApp(t)
			ctx := context.Background()
			base.entries["root"] = []drives.Entry{
				{ID: "retry-dir", Name: "Retry", IsDir: true},
				{ID: "live-file", Name: "live.mp4", Size: 123},
			}
			base.entries["retry-dir"] = []drives.Entry{
				{ID: "existing-file", Name: "existing.mp4", Size: 234},
				{ID: "recovered-file", Name: "recovered.mp4", Size: 345},
			}
			now := time.Now()
			if err := app.cat.UpsertVideo(ctx, &catalog.Video{
				ID: "existing-row", DriveID: base.ID(), FileID: "existing-file", FileName: "existing.mp4",
				Title: "Existing", Size: 234, ParentID: "retry-dir", AncestorDirIDs: []string{"root", "retry-dir"},
				CreatedAt: now, UpdatedAt: now, PublishedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			drv := &finalPassScanDrive{serverTreeScanDrive: base, failures: tt.failures}
			app.registry.Set(drv.ID(), drv)
			// Two scans also exercise the guarded cleanup confirmation threshold:
			// an unreadable subtree must never count as a missing source.
			for round := 0; round < 2; round++ {
				drv.calls = 0
				result := app.runScan(ctx, drv.ID())
				wantAdded := 0
				if round == 0 {
					wantAdded = tt.firstAdded
				}
				if result.State != tt.state || result.ErrorCount != tt.errors || len(result.Issues) != tt.errors ||
					result.ScannedCount != tt.scanned || result.AddedCount != wantAdded || drv.calls != tt.calls {
					t.Fatalf("round %d: result=%+v directory calls=%d", round, result, drv.calls)
				}
				stored, err := app.cat.LatestScanResults(ctx)
				if err != nil || stored[drv.ID()].State != tt.state || stored[drv.ID()].ErrorCount != tt.errors {
					t.Fatalf("stored result=%+v err=%v", stored, err)
				}
				if _, err := app.cat.GetVideo(ctx, "existing-row"); err != nil {
					t.Fatalf("existing file was removed after directory retries: %v", err)
				}
			}
		})
	}
}

func TestScanReportsFatalFailureAndCancellation(t *testing.T) {
	t.Run("root failure", func(t *testing.T) {
		app, drv := scanResultTestApp(t)
		drv.listErrors = map[string]error{"root": errors.New("root unavailable")}
		result := app.runScan(context.Background(), drv.ID())
		if result.State != scanjob.Failed || result.ErrorCount != 1 || result.Message == "" {
			t.Fatalf("result = %+v", result)
		}
		stored, err := app.cat.LatestScanResults(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stored[drv.ID()].State != scanjob.Failed {
			t.Fatalf("stored = %+v", stored)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		app, drv := scanResultTestApp(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result := app.runScan(ctx, drv.ID())
		if result.State != scanjob.Canceled {
			t.Fatalf("result = %+v", result)
		}
		stored, err := app.cat.LatestScanResults(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stored[drv.ID()].State != scanjob.Canceled {
			t.Fatalf("cancellation was not retained: %+v", stored)
		}
	})
}

func TestScanReturnsSkippedWhenAnotherScanOwnsTheDrive(t *testing.T) {
	app, drv := scanResultTestApp(t)
	if !app.beginDriveScanOrCrawl(drv.ID()) {
		t.Fatal("could not begin original scan")
	}
	defer app.endDriveScanOrCrawl(drv.ID())
	result := app.runScan(context.Background(), drv.ID())
	if result.State != scanjob.Skipped || result.Message == "" {
		t.Fatalf("result = %+v", result)
	}
	if !app.scanQueued[drv.ID()] {
		t.Fatal("rejected scan cleared the running scan")
	}
}

type generationResetScanDrive struct {
	*serverTreeScanDrive
	resets int
	onList func()
}

func (d *generationResetScanDrive) ResetGenerationStreamForScan() { d.resets++ }

func (d *generationResetScanDrive) List(ctx context.Context, dirID string) ([]drives.Entry, error) {
	d.onList()
	return d.serverTreeScanDrive.List(ctx, dirID)
}

func TestScanResetsGenerationStreamOnceBeforeDiscovery(t *testing.T) {
	app, base := scanResultTestApp(t)
	drv := &generationResetScanDrive{serverTreeScanDrive: base}
	app.registry.Set(drv.ID(), drv)
	for round := 1; round <= 2; round++ {
		drv.onList = func() {
			if drv.resets != round {
				t.Errorf("resets before discovery=%d, want %d", drv.resets, round)
			}
		}
		if result := app.runScan(context.Background(), drv.ID()); result.State != scanjob.Succeeded {
			t.Fatalf("scan: %+v", result)
		}
		if drv.resets != round {
			t.Fatalf("resets=%d, want %d", drv.resets, round)
		}
	}
	if !app.beginDriveScanOrCrawl(drv.ID()) {
		t.Fatal("could not reserve scan")
	}
	defer app.endDriveScanOrCrawl(drv.ID())
	if result := app.runScan(context.Background(), drv.ID()); result.State != scanjob.Skipped {
		t.Fatalf("duplicate scan: %+v", result)
	}
	if drv.resets != 2 {
		t.Fatal("skipped scan reset the active session")
	}
}
