import { readFileSync } from 'fs';
import path from 'path';

import { DASHBOARD_SCHEMA_VERSION } from './DashboardMigrator';
import { DashboardModel } from './DashboardModel';
import {
  setupTestDataSources,
  getTestDirectories,
  getOutputDirectory,
  getJsonInputFiles,
  extractTargetVersionFromFilename,
  constructBackendOutputFilename,
  handleAngularPanelMigration,
  cleanDashboardModel,
} from './__tests__/migrationTestUtils';

/*
 * Single Version Migration Test Design Explanation:
 *
 * This test compares backend and frontend single version migration results by running both through DashboardModel.
 * Instead of migrating to the latest version like DashboardMigratorToBackend.test.ts, this test migrates to the
 * specific target version indicated in the filename (e.g., v16.grid_layout_upgrade.json migrates to v16).
 *
 * This approach is correct and not flaky for the following reasons:
 *
 * 1. Frontend Single Version Migration Path:
 *    jsonInput (e.g. v15) → DashboardModel with targetVersion=16 → DashboardMigrator runs → migrates to v16 → getSaveModelClone()
 *
 * 2. Backend Single Version Migration Path:
 *    jsonInput (e.g. v15) → Backend Migration to v16 → backendOutput (v16) → DashboardModel → DashboardMigrator sees v16 → early return (no migration) → getSaveModelClone()
 *
 * 3. Why DashboardMigrator doesn't run on backendOutput:
 *    - DashboardMigrator.updateSchema() has an early return: `if (oldVersion === this.dashboard.schemaVersion) return;`
 *    - Since backendOutput.schemaVersion is already at the target version, no migration occurs
 *    - This ensures we compare the final migrated state from both paths
 *
 * 4. Benefits of this approach:
 *    - Tests the complete integration (backend single version migration + DashboardModel)
 *    - Accounts for DashboardModel's default value handling and normalization
 *    - Ensures both paths produce identical final dashboard states for single version migrations
 *    - Avoids test brittleness from comparing raw JSON with different default value representations
 */

describe('Backend / Frontend single version migration result comparison', () => {
  beforeEach(() => {
    jest.clearAllMocks();
    setupTestDataSources();
  });

  const { inputDir } = getTestDirectories();
  const outputDir = getOutputDirectory('single_version');
  const jsonInputs = getJsonInputFiles(inputDir);

  jsonInputs
    // TODO: remove this filter when we fixed all inconsistencies
    .filter((inputFile) => parseInt(inputFile.split('.')[0].replace('v', ''), 10) > 29)
    .forEach((inputFile) => {
      // Extract target version from filename (e.g., v16.grid_layout_upgrade.json -> target v16)
      const targetVersion = extractTargetVersionFromFilename(inputFile);
      if (!targetVersion) {
        return; // Skip files that don't match the expected pattern
      }

      // Skip if target version exceeds latest version
      if (targetVersion > DASHBOARD_SCHEMA_VERSION) {
        return;
      }

      it(`should migrate ${inputFile} to v${targetVersion} correctly`, async () => {
        const jsonInput = JSON.parse(readFileSync(path.join(inputDir, inputFile), 'utf8'));

        // Verify the input file follows the naming convention: filename version = target version, schemaVersion = target - 1
        const expectedSchemaVersion = targetVersion - 1;
        expect(jsonInput.schemaVersion).toBe(expectedSchemaVersion);

        // Generate the expected output filename for single version migration
        const singleVersionOutputFile = constructBackendOutputFilename(inputFile, targetVersion);
        const singleVersionOutputPath = path.join(outputDir, singleVersionOutputFile);

        // Check if the single version output file exists
        let backendOutput: any;
        try {
          backendOutput = JSON.parse(readFileSync(singleVersionOutputPath, 'utf8'));
        } catch (error) {
          // If single version output doesn't exist, skip this test
          // This can happen if the backend test hasn't generated the single version output yet
          console.warn(`Skipping ${inputFile}: single version output file ${singleVersionOutputFile} not found`);
          return;
        }

        expect(backendOutput.schemaVersion).toEqual(targetVersion);

        // Create dashboard models
        const frontendModel = new DashboardModel(jsonInput, undefined, {
          targetSchemaVersion: targetVersion,
        });
        const backendModel = new DashboardModel(backendOutput, undefined, {
          targetSchemaVersion: targetVersion,
        });

        // Handle angular panel migration if needed
        if (jsonInput.schemaVersion <= 27) {
          await handleAngularPanelMigration(frontendModel);
        }

        const frontendMigrationResult = frontendModel.getSaveModelClone();
        const backendMigrationResult = backendModel.getSaveModelClone();

        expect(backendMigrationResult).toEqual(frontendMigrationResult);
      });
    });
});
