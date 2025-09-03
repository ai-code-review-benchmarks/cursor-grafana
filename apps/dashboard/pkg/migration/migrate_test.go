package migration_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/grafana/grafana/apps/dashboard/pkg/migration"
	"github.com/grafana/grafana/apps/dashboard/pkg/migration/schemaversion"
	"github.com/grafana/grafana/apps/dashboard/pkg/migration/testutil"
)

const INPUT_DIR = "testdata/input"
const OUTPUT_DIR = "testdata/output"
const SINGLE_VERSION_OUTPUT_DIR = "testdata/output/single_version"
const LATEST_VERSION_OUTPUT_DIR = "testdata/output/latest_version"

func TestMigrate(t *testing.T) {
	files, err := os.ReadDir(INPUT_DIR)
	require.NoError(t, err)

	// Use the same datasource provider as the frontend test to ensure consistency
	migration.Initialize(testutil.GetTestDataSourceProvider(), testutil.GetTestPanelProvider())

	t.Run("minimum version check", func(t *testing.T) {
		err := migration.Migrate(map[string]interface{}{
			"schemaVersion": schemaversion.MIN_VERSION - 1,
		}, schemaversion.MIN_VERSION)

		var minVersionErr = schemaversion.NewMinimumVersionError(schemaversion.MIN_VERSION - 1)
		require.ErrorAs(t, err, &minVersionErr)
	})

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		// Validate filename format
		if !strings.HasPrefix(f.Name(), "v") || !strings.HasSuffix(f.Name(), ".json") {
			t.Fatalf("input filename must use v{N}.{name}.json format, got: %s", f.Name())
		}

		versionStr := strings.TrimPrefix(f.Name(), "v")
		dotIndex := strings.Index(versionStr, ".")
		if dotIndex == -1 {
			t.Fatalf("input filename must use v{N}.{name}.json format, got: %s", f.Name())
		}

		filenameTargetVersion, err := strconv.Atoi(versionStr[:dotIndex])
		require.NoError(t, err, "failed to parse version from filename: %s", f.Name())

		inputDash := loadDashboard(t, filepath.Join(INPUT_DIR, f.Name()))
		inputVersion := getSchemaVersion(t, inputDash)

		// Validate naming convention: filename version should be the tested version, schemaVersion should be target - 1
		expectedSchemaVersion := filenameTargetVersion - 1
		require.Equal(t, expectedSchemaVersion, inputVersion,
			"naming convention violation for %s: filename suggests target v%d, but schemaVersion is %d (should be %d)",
			f.Name(), filenameTargetVersion, inputVersion, expectedSchemaVersion)

		t.Run("input check "+f.Name(), func(t *testing.T) {
			// use input version as the target version to ensure there are no changes
			require.NoError(t, migration.Migrate(inputDash, inputVersion), "input check migration failed")
			outBytes, err := json.MarshalIndent(inputDash, "", "  ")
			require.NoError(t, err, "failed to marshal migrated dashboard")
			// We can ignore gosec G304 here since it's a test
			// nolint:gosec
			expectedDash, err := os.ReadFile(filepath.Join(INPUT_DIR, f.Name()))
			require.NoError(t, err, "failed to read expected output file")
			require.JSONEq(t, string(expectedDash), string(outBytes), "%s input check did not match", f.Name())
		})

		testName := fmt.Sprintf("%s v%d to v%d", f.Name(), inputVersion, schemaversion.LATEST_VERSION)
		t.Run(testName, func(t *testing.T) {
			testMigration(t, inputDash, f.Name(), schemaversion.LATEST_VERSION)
		})
	}
}

func TestMigrateSingleVersion(t *testing.T) {
	files, err := os.ReadDir(INPUT_DIR)
	require.NoError(t, err)

	// Use the same datasource provider as the frontend test to ensure consistency
	migration.Initialize(testutil.GetTestDataSourceProvider(), testutil.GetTestPanelProvider())

	for _, f := range files {
		if f.IsDir() {
			continue
		}

		// Validate filename format
		if !strings.HasPrefix(f.Name(), "v") || !strings.HasSuffix(f.Name(), ".json") {
			t.Fatalf("input filename must use v{N}.{name}.json format, got: %s", f.Name())
		}

		// Extract version from filename (e.g., v16.grid_layout_upgrade.json -> 16)
		// This represents the tested version that the file should be migrated TO
		versionStr := strings.TrimPrefix(f.Name(), "v")
		dotIndex := strings.Index(versionStr, ".")
		if dotIndex == -1 {
			t.Fatalf("input filename must use v{N}.{name}.json format, got: %s", f.Name())
		}

		targetVersion, err := strconv.Atoi(versionStr[:dotIndex])
		require.NoError(t, err, "failed to parse version from filename: %s", f.Name())

		// Skip if target version exceeds latest version
		if targetVersion > schemaversion.LATEST_VERSION {
			t.Skipf("skipping %s: target version %d exceeds latest version %d", f.Name(), targetVersion, schemaversion.LATEST_VERSION)
		}

		inputDash := loadDashboard(t, filepath.Join(INPUT_DIR, f.Name()))
		inputVersion := getSchemaVersion(t, inputDash)

		// Validate naming convention: filename version should be target version, schemaVersion should be target - 1
		expectedSchemaVersion := targetVersion - 1
		require.Equal(t, expectedSchemaVersion, inputVersion,
			"naming convention violation for %s: filename suggests target v%d, but schemaVersion is %d (should be %d)",
			f.Name(), targetVersion, inputVersion, expectedSchemaVersion)

		// File follows the expected pattern: current version is one less than target
		testName := fmt.Sprintf("%s v%d to v%d", f.Name(), inputVersion, targetVersion)
		t.Run(testName, func(t *testing.T) {
			testSingleMigration(t, inputDash, f.Name(), inputVersion, targetVersion)
		})
	}
}

func testSingleMigration(t *testing.T, dash map[string]interface{}, inputFileName string, inputVersion, targetVersion int) {
	t.Helper()

	// Verify input version matches filename
	actualInputVersion := getSchemaVersion(t, dash)
	require.Equal(t, inputVersion, actualInputVersion, "input version mismatch for %s", inputFileName)

	// Run migration to target version
	require.NoError(t, migration.Migrate(dash, targetVersion), "atomic migration from v%d to v%d failed", inputVersion, targetVersion)

	// Verify final schema version
	finalVersion := getSchemaVersion(t, dash)
	require.Equal(t, targetVersion, finalVersion, "dashboard not migrated to target version %d", targetVersion)

	// Generate output filename with target version suffix
	// e.g., v16.grid_layout_upgrade.json -> v16.grid_layout_upgrade.v16.json
	outputFileName := strings.TrimSuffix(inputFileName, ".json") + fmt.Sprintf(".v%d.json", targetVersion)
	outPath := filepath.Join(SINGLE_VERSION_OUTPUT_DIR, outputFileName)

	outBytes, err := json.MarshalIndent(dash, "", "  ")
	require.NoError(t, err, "failed to marshal migrated dashboard")

	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		err = os.WriteFile(outPath, outBytes, 0644)
		require.NoError(t, err, "failed to write new output file", outPath)
		return
	}

	// We can ignore gosec G304 here since it's a test
	// nolint:gosec
	existingBytes, err := os.ReadFile(outPath)
	require.NoError(t, err, "failed to read existing output file")
	require.JSONEq(t, string(existingBytes), string(outBytes), "%s did not match", outPath)
}

func testMigration(t *testing.T, dash map[string]interface{}, inputFileName string, targetVersion int) {
	t.Helper()
	require.NoError(t, migration.Migrate(dash, targetVersion), "migration to v%d failed", targetVersion)
	finalVersion := getSchemaVersion(t, dash)
	require.Equal(t, targetVersion, finalVersion, "dashboard not migrated to target version %d", targetVersion)

	// Generate output filename with target version suffix: e.g., v16.grid_layout_upgrade.json -> v16.grid_layout_upgrade.v41.json
	outputFileName := strings.TrimSuffix(inputFileName, ".json") + fmt.Sprintf(".v%d.json", targetVersion)
	outPath := filepath.Join(LATEST_VERSION_OUTPUT_DIR, outputFileName)

	outBytes, err := json.MarshalIndent(dash, "", "  ")
	require.NoError(t, err, "failed to marshal migrated dashboard")

	if _, err := os.Stat(outPath); os.IsNotExist(err) {
		err = os.WriteFile(outPath, outBytes, 0644)
		require.NoError(t, err, "failed to write new output file", outPath)
		return
	}
	existingBytes, err := os.ReadFile(outPath)
	require.NoError(t, err, "failed to read existing output file")
	require.JSONEq(t, string(existingBytes), string(outBytes), "%s did not match", outPath)
}

func getSchemaVersion(t *testing.T, dash map[string]interface{}) int {
	t.Helper()
	version, ok := dash["schemaVersion"]
	require.True(t, ok, "dashboard missing schemaVersion")

	switch v := version.(type) {
	case int:
		return v
	case float64:
		return int(v)
	default:
		t.Fatalf("invalid schemaVersion type: %T", version)
		return 0
	}
}

func loadDashboard(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	// We can ignore gosec G304 here since it's a test
	// nolint:gosec
	inputBytes, err := os.ReadFile(path)
	require.NoError(t, err, "failed to read input file")

	var dash map[string]interface{}
	require.NoError(t, json.Unmarshal(inputBytes, &dash), "failed to unmarshal dashboard JSON")
	return dash
}
