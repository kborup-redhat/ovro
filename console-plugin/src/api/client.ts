import { consoleFetch } from '@openshift-console/dynamic-plugin-sdk';
import {
  RightsizingRecommendation,
  OverviewData,
  RightsizingPolicy,
  RightsizingPolicySpec,
  RestartOption,
} from '../types';

const API_BASE = '/api/proxy/plugin/ovro-console-plugin/ovro-backend/api/v1';

async function fetchJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await consoleFetch(url, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options?.headers,
    },
  });
  if (!response.ok) {
    const text = await response.text();
    throw new Error(`API error ${response.status}: ${text}`);
  }
  return response.json();
}

export async function listRecommendations(filters?: {
  namespace?: string;
  direction?: string;
  state?: string;
}): Promise<RightsizingRecommendation[]> {
  const params = new URLSearchParams();
  if (filters?.namespace) params.set('namespace', filters.namespace);
  if (filters?.direction) params.set('direction', filters.direction);
  if (filters?.state) params.set('state', filters.state);
  const query = params.toString() ? `?${params}` : '';
  return fetchJSON(`${API_BASE}/recommendations${query}`);
}

export async function getRecommendation(
  namespace: string,
  name: string,
): Promise<RightsizingRecommendation> {
  return fetchJSON(`${API_BASE}/recommendations/${namespace}/${name}`);
}

export async function applyRecommendation(
  namespace: string,
  name: string,
  restartOption: RestartOption,
  scheduledAt?: string,
): Promise<RightsizingRecommendation> {
  return fetchJSON(`${API_BASE}/recommendations/${namespace}/${name}/apply`, {
    method: 'POST',
    body: JSON.stringify({ restartOption, scheduledAt }),
  });
}

export async function revertRecommendation(
  namespace: string,
  name: string,
): Promise<RightsizingRecommendation> {
  return fetchJSON(`${API_BASE}/recommendations/${namespace}/${name}/revert`, {
    method: 'POST',
  });
}

export async function excludeVM(namespace: string, name: string): Promise<void> {
  await fetchJSON(`${API_BASE}/vms/${namespace}/${name}/exclude`, {
    method: 'POST',
  });
}

export async function removeExclusion(namespace: string, name: string): Promise<void> {
  await fetchJSON(`${API_BASE}/vms/${namespace}/${name}/exclude`, {
    method: 'DELETE',
  });
}

export async function getOverview(): Promise<OverviewData> {
  return fetchJSON(`${API_BASE}/overview`);
}

export async function getPolicy(): Promise<RightsizingPolicy> {
  return fetchJSON(`${API_BASE}/policy`);
}

export async function updatePolicy(spec: RightsizingPolicySpec): Promise<RightsizingPolicy> {
  return fetchJSON(`${API_BASE}/policy`, {
    method: 'PUT',
    body: JSON.stringify(spec),
  });
}
