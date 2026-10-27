/**
 * 出网代理节点 API（对接 cpamp 内置的 mihomo 节点面板接口）
 */

import axios from 'axios';
import { REQUEST_TIMEOUT_MS } from '@/utils/constants';
import { normalizeApiBase } from '@/utils/connection';

export interface ProxyHistoryPoint {
  time: string;
  delay: number;
}

export interface ProxyNode {
  name: string;
  type: string;
  delay: number;
  history: ProxyHistoryPoint[];
  alive: boolean;
  inAuto: boolean;
  selected: boolean;
  autoSelected: boolean;
  udp: boolean;
  provider?: string;
}

export interface ProxyGroupInfo {
  name: string;
  type: string;
  now: string;
  alive: boolean;
  count: number;
}

export interface ProxyNodesOverview {
  enabled: boolean;
  available: boolean;
  canEdit: boolean;
  error?: string;
  group: ProxyGroupInfo;
  autoGroup: ProxyGroupInfo | null;
  nodes: ProxyNode[];
  testUrl: string;
  collectedAt: number;
}

export interface ProxyDelayResult {
  name: string;
  delay: number;
  error?: string;
}

export interface ProxySource {
  name: string;
  type: string;
  url?: string;
  intervalSeconds?: number;
  updatedAt?: string;
  nodeCount: number;
  excluded?: string[];
  builtin: boolean;
}

export interface ProxyHealthCheckResult {
  delays: Record<string, number>;
  overview?: ProxyNodesOverview;
  providers: Array<{ name: string; error?: string }>;
}

export interface ImportNodesResult {
  ok: boolean;
  added: number;
  total: number;
  failures: string[];
}

const NODES_PATH = '/v0/management/proxy-nodes';
const REQUEST_TIMEOUT_MS_LOCAL = Math.max(REQUEST_TIMEOUT_MS, 20 * 1000);

const buildUrl = (base: string, path: string): string =>
  `${normalizeApiBase(base).replace(/\/+$/, '')}${path}`;

const authHeaders = (managementKey?: string) =>
  managementKey ? { Authorization: `Bearer ${managementKey}` } : undefined;

const readErrorMessage = (error: unknown): string => {
  if (axios.isAxiosError(error)) {
    const data = error.response?.data;
    if (
      data &&
      typeof data === 'object' &&
      typeof (data as { error?: unknown }).error === 'string'
    ) {
      return (data as { error: string }).error;
    }
    return error.message;
  }
  return error instanceof Error ? error.message : String(error);
};

const request = async <T>(run: () => Promise<T>): Promise<T> => {
  try {
    return await run();
  } catch (error) {
    throw new Error(readErrorMessage(error));
  }
};

export const proxyNodesApi = {
  getOverview: (base: string, managementKey?: string): Promise<ProxyNodesOverview> =>
    request(async () => {
      const response = await axios.get<ProxyNodesOverview>(buildUrl(base, NODES_PATH), {
        timeout: REQUEST_TIMEOUT_MS_LOCAL,
        headers: authHeaders(managementKey),
      });
      return response.data;
    }),

  select: (
    base: string,
    name: string,
    managementKey?: string
  ): Promise<ProxyNodesOverview | null> =>
    request(async () => {
      const response = await axios.post<{ ok: boolean; overview?: ProxyNodesOverview }>(
        buildUrl(base, `${NODES_PATH}/select`),
        { name },
        { timeout: REQUEST_TIMEOUT_MS_LOCAL, headers: authHeaders(managementKey) }
      );
      return response.data?.overview ?? null;
    }),

  test: (
    base: string,
    names: string[] | undefined,
    managementKey?: string
  ): Promise<ProxyDelayResult[]> =>
    request(async () => {
      const response = await axios.post<{ results: ProxyDelayResult[] }>(
        buildUrl(base, `${NODES_PATH}/test`),
        { names: names ?? [], timeoutMs: 5000 },
        { timeout: REQUEST_TIMEOUT_MS_LOCAL, headers: authHeaders(managementKey) }
      );
      return response.data?.results ?? [];
    }),

  healthCheck: (
    base: string,
    managementKey?: string,
    timeoutMs = 5000
  ): Promise<ProxyHealthCheckResult> =>
    request(async () => {
      const response = await axios.post<ProxyHealthCheckResult>(
        buildUrl(base, `${NODES_PATH}/healthcheck`),
        { timeoutMs },
        { timeout: 60 * 1000, headers: authHeaders(managementKey) }
      );
      return response.data;
    }),

  listSources: (base: string, managementKey?: string): Promise<ProxySource[]> =>
    request(async () => {
      const response = await axios.get<{ sources: ProxySource[] }>(
        buildUrl(base, `${NODES_PATH}/sources`),
        { timeout: REQUEST_TIMEOUT_MS_LOCAL, headers: authHeaders(managementKey) }
      );
      return response.data?.sources ?? [];
    }),

  addSubscription: (
    base: string,
    payload: { name?: string; url: string; intervalSeconds?: number },
    managementKey?: string
  ): Promise<string> =>
    request(async () => {
      const response = await axios.post<{ name: string }>(
        buildUrl(base, `${NODES_PATH}/sources`),
        payload,
        { timeout: 60 * 1000, headers: authHeaders(managementKey) }
      );
      return response.data?.name ?? '';
    }),

  updateSource: (base: string, name: string, managementKey?: string): Promise<void> =>
    request(async () => {
      await axios.post(
        buildUrl(base, `${NODES_PATH}/sources/${encodeURIComponent(name)}/update`),
        {},
        { timeout: 60 * 1000, headers: authHeaders(managementKey) }
      );
    }),

  removeSource: (base: string, name: string, managementKey?: string): Promise<void> =>
    request(async () => {
      await axios.delete(buildUrl(base, `${NODES_PATH}/sources/${encodeURIComponent(name)}`), {
        timeout: REQUEST_TIMEOUT_MS_LOCAL,
        headers: authHeaders(managementKey),
      });
    }),

  importNodes: (
    base: string,
    payload: { links?: string[]; yaml?: string },
    managementKey?: string
  ): Promise<ImportNodesResult> =>
    request(async () => {
      const response = await axios.post<ImportNodesResult>(
        buildUrl(base, `${NODES_PATH}/nodes`),
        payload,
        { timeout: 60 * 1000, headers: authHeaders(managementKey) }
      );
      return response.data;
    }),

  removeNode: (
    base: string,
    provider: string,
    name: string,
    managementKey?: string
  ): Promise<void> =>
    request(async () => {
      await axios.delete(
        buildUrl(
          base,
          `${NODES_PATH}/sources/${encodeURIComponent(provider)}/nodes/${encodeURIComponent(name)}`
        ),
        { timeout: REQUEST_TIMEOUT_MS_LOCAL, headers: authHeaders(managementKey) }
      );
    }),

  restoreSource: (base: string, provider: string, managementKey?: string): Promise<void> =>
    request(async () => {
      await axios.post(
        buildUrl(base, `${NODES_PATH}/sources/${encodeURIComponent(provider)}/restore`),
        {},
        { timeout: REQUEST_TIMEOUT_MS_LOCAL, headers: authHeaders(managementKey) }
      );
    }),
};
