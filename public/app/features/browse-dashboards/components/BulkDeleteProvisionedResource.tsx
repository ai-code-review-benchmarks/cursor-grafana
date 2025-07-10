import { FormProvider, useForm } from 'react-hook-form';

import { AppEvents } from '@grafana/data';
import { Trans, t } from '@grafana/i18n';
import { getAppEvents } from '@grafana/runtime';
import { Box, Button, Stack } from '@grafana/ui';
import {
  DeleteRepositoryFilesWithPathApiArg,
  DeleteRepositoryFilesWithPathApiResponse,
  RepositoryView,
  useDeleteRepositoryFilesWithPathMutation,
} from 'app/api/clients/provisioning/v0alpha1';
import { AnnoKeySourcePath } from 'app/features/apiserver/types';
import { ResourceEditFormSharedFields } from 'app/features/dashboard-scene/components/Provisioned/ResourceEditFormSharedFields';
import { getDefaultWorkflow, getWorkflowOptions } from 'app/features/dashboard-scene/saving/provisioned/defaults';
import { generateTimestamp } from 'app/features/dashboard-scene/saving/provisioned/utils/timestamp';
import { useGetResourceRepositoryView } from 'app/features/provisioning/hooks/useGetResourceRepositoryView';
import { WorkflowOption } from 'app/features/provisioning/types';

import { useChildrenByParentUIDState } from '../state/hooks';
import { findItem } from '../state/utils';
import { DashboardTreeSelection } from '../types';

import { DescendantCount } from './BrowseActions/DescendantCount';

interface BulkDeleteFormData {
  comment: string;
  ref: string;
  workflow?: WorkflowOption;
}

interface FormProps extends BulkDeleteProvisionResourceProps {
  initialValues: BulkDeleteFormData;
  repository: RepositoryView;
  workflowOptions: Array<{ label: string; value: string }>;
  isGitHub: boolean;
  folderPath?: string;
}

interface BulkDeleteProvisionResourceProps {
  folderUid?: string;
  selectedItems: Omit<DashboardTreeSelection, 'panel' | '$all'>;
  onDismiss?: () => void;
}

type BulkSuccessResponse = Array<{
  index: number;
  item: DeleteRepositoryFilesWithPathApiArg;
  data: DeleteRepositoryFilesWithPathApiResponse;
}>;
type BulkFailureResponse = Array<{ index: number; item: DeleteRepositoryFilesWithPathApiArg; error: unknown }>;

function FormContent({
  initialValues,
  selectedItems,
  repository,
  workflowOptions,
  folderPath,
  isGitHub,
  onDismiss,
}: FormProps) {
  const [deleteRepoFile, request] = useDeleteRepositoryFilesWithPathMutation();

  const methods = useForm<BulkDeleteFormData>({ defaultValues: initialValues });
  const childrenByParentUID = useChildrenByParentUIDState();
  const { handleSubmit, watch } = methods;
  const workflow = watch('workflow');

  const getPath = (uid: string, isFolder: boolean): string => {
    // Find the dashboard item by UID
    const item = findItem([], childrenByParentUID, uid);

    if (!item) {
      return '';
    }

    if (isFolder) {
      return `${folderPath}/${item.title}/`;
    } else {
      const dashboardTitle = item.title;
      return `${folderPath}/${dashboardTitle}.json`;
    }
  };

  const handleSuccess = (successes: BulkSuccessResponse) => {
    getAppEvents().publish({
      type: AppEvents.alertSuccess.name,
      payload: [
        t('browse-dashboard.bulk-delete-resources-form.api-success', `Successfully deleted ${successes.length} items`),
      ],
    });

    if (workflow === 'branch') {
      // when push to new branch, we reload page with repo_url
      const url = new URL(window.location.href);
      const repoUrl = successes[0].data.urls?.repositoryURL;
      if (repoUrl) {
        url.searchParams.set('repo_url', repoUrl);
        window.location.href = url.toString();
      }
      onDismiss?.();
      window.location.reload();
    } else {
      // if workflow is 'write'
      onDismiss?.();
      window.location.reload();
    }
  };

  const handleSubmitForm = async (data: BulkDeleteFormData) => {
    const buildDeleteParams = (
      items: Record<string, boolean | undefined>,
      isFolder: boolean
    ): DeleteRepositoryFilesWithPathApiArg[] => {
      return Object.keys(items).map((key) => {
        const path = getPath(key, isFolder);
        return {
          name: repository.name,
          path,
          ref: workflow === 'write' ? undefined : data.ref,
          message: data.comment || `Delete resources ${path}`,
        };
      });
    };

    const deleteRequests = [
      ...buildDeleteParams(selectedItems.folder, true),
      ...buildDeleteParams(selectedItems.dashboard, false),
    ];

    const results = await Promise.allSettled(deleteRequests.map((params) => deleteRepoFile(params).unwrap()));

    const successes: BulkSuccessResponse = [];
    const failures: BulkFailureResponse = [];

    results.forEach((result, index) => {
      const item = deleteRequests[index];
      if (result.status === 'fulfilled') {
        successes.push({ index, item, data: result.value });
      } else {
        failures.push({ index, item, error: result.reason });
      }
    });

    if (successes.length > 0) {
      handleSuccess(successes);
    }

    if (failures.length > 0) {
      getAppEvents().publish({
        type: AppEvents.alertError.name,
        payload: [
          t('browse-dashboard.bulk-delete-resources-form.api-error', `Failed to delete ${failures.length} items`),
          failures.map((f) => `${f.item.path}`).join('\n'),
        ],
      });
    }
  };

  return (
    <FormProvider {...methods}>
      <form onSubmit={handleSubmit(handleSubmitForm)}>
        <Stack direction="column" gap={2}>
          <Box paddingBottom={2}>
            <Trans i18nKey="browse-dashboards.bulk-delete-resources-form.delete-warning">
              This will delete selected folders and their descendants. In total, this will affect:
            </Trans>
            <DescendantCount selectedItems={{ ...selectedItems, panel: {}, $all: false }} />
          </Box>

          <ResourceEditFormSharedFields
            resourceType="folder"
            isNew={false}
            workflow={workflow}
            workflowOptions={workflowOptions}
            isGitHub={isGitHub}
            hidePath
          />

          {/* Delete / Cancel button */}
          <Stack gap={2}>
            <Button type="submit" disabled={request.isLoading} variant="destructive">
              {request.isLoading
                ? t('browse-dashboards.bulk-delete-resources-form.button-deleting', 'Deleting...')
                : t('browse-dashboards.bulk-delete-resources-form.button-delete', 'Delete')}
            </Button>
            <Button variant="secondary" fill="outline" onClick={onDismiss}>
              <Trans i18nKey="browse-dashboards.bulk-delete-resources-form.button-cancel">Cancel</Trans>
            </Button>
          </Stack>
        </Stack>
      </form>
    </FormProvider>
  );
}

export function BulkDeleteProvisionedResource({
  folderUid,
  selectedItems,
  onDismiss,
}: BulkDeleteProvisionResourceProps) {
  const { repository, folder } = useGetResourceRepositoryView({ folderName: folderUid });

  const workflowOptions = getWorkflowOptions(repository);
  const isGitHub = repository?.type === 'github';
  const folderPath = folder?.metadata?.annotations?.[AnnoKeySourcePath] || '';
  const timestamp = generateTimestamp();

  const initialValues = {
    comment: '',
    ref: `bulk-delete/${timestamp}`,
    workflow: getDefaultWorkflow(repository),
  };

  if (!repository) {
    return null;
  }

  return (
    <FormContent
      selectedItems={selectedItems}
      onDismiss={onDismiss}
      initialValues={initialValues}
      repository={repository}
      workflowOptions={workflowOptions}
      isGitHub={isGitHub}
      folderPath={folderPath}
    />
  );
}
