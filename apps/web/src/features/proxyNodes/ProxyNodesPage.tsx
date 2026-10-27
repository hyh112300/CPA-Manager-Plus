import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { Drawer } from '@/components/ui/Drawer';
import { Input } from '@/components/ui/Input';
import { SegmentedTabs, type SegmentedTabItem } from '@/components/ui/SegmentedTabs';
import { LoadingSpinner } from '@/components/ui/LoadingSpinner';
import {
  IconCheck,
  IconPlus,
  IconRotateCcw,
  IconSatellite,
  IconTrash2,
  IconTriangleAlert,
  IconX,
} from '@/components/ui/icons';
import { useHeaderRefresh } from '@/hooks/useHeaderRefresh';
import { usePanelFeatureAvailability } from '@/hooks/usePanelFeatureAvailability';
import { useAuthStore, useNotificationStore } from '@/stores';
import {
  proxyNodesApi,
  type ProxyNode,
  type ProxyNodesOverview,
  type ProxySource,
} from '@/services/api/proxyNodes';
import styles from './ProxyNodesPage.module.scss';

const AUTO_REFRESH_INTERVAL_MS = 20_000;
const IMPORTED_PROVIDER = 'imported';

const PROTOCOL_LABELS: Record<string, string> = {
  ss: 'Shadowsocks',
  shadowsocks: 'Shadowsocks',
  ssr: 'ShadowsocksR',
  vmess: 'VMess',
  vless: 'VLESS',
  trojan: 'Trojan',
  hysteria: 'Hysteria',
  hysteria2: 'Hysteria2',
  tuic: 'TUIC',
  socks5: 'SOCKS5',
  http: 'HTTP',
  wireguard: 'WireGuard',
  selector: 'Selector',
  fallback: 'Fallback',
  urltest: 'URLTest',
  'url-test': 'URLTest',
  loadbalance: 'LoadBalance',
  direct: 'DIRECT',
};

const protocolLabel = (type: string): string =>
  PROTOCOL_LABELS[type.toLowerCase()] ?? type.toUpperCase();

type ImportTab = 'subscription' | 'links' | 'yaml';

export function ProxyNodesPage() {
  const connectionScope = useAuthStore((state) =>
    JSON.stringify([state.apiBase, state.managementKey])
  );
  const { managerServiceBase } = usePanelFeatureAvailability();
  return <ProxyNodesContent key={`${connectionScope}:${managerServiceBase}`} />;
}

function ProxyNodesContent() {
  const { t } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const featureAvailability = usePanelFeatureAvailability();
  const managementKey = useAuthStore((state) => state.managementKey);

  const [overview, setOverview] = useState<ProxyNodesOverview | null>(null);
  const [sources, setSources] = useState<ProxySource[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [pendingName, setPendingName] = useState('');
  const [checking, setChecking] = useState(false);
  const [busySource, setBusySource] = useState('');
  const [importOpen, setImportOpen] = useState(false);
  const requestId = useRef(0);

  const base = featureAvailability.managerServiceBase;
  const ready = featureAvailability.managerServiceAvailable && Boolean(base) && Boolean(managementKey);

  useEffect(
    () => () => {
      requestId.current += 1;
    },
    []
  );

  const load = useCallback(async () => {
    if (!ready) {
      setOverview(null);
      setSources([]);
      setError('');
      return;
    }
    const currentRequest = requestId.current + 1;
    requestId.current = currentRequest;
    setLoading(true);
    setError('');
    try {
      const [nextOverview, nextSources] = await Promise.all([
        proxyNodesApi.getOverview(base, managementKey),
        proxyNodesApi.listSources(base, managementKey).catch(() => [] as ProxySource[]),
      ]);
      if (requestId.current !== currentRequest) return;
      setOverview(nextOverview);
      setSources(nextSources);
    } catch (err: unknown) {
      if (requestId.current !== currentRequest) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (requestId.current === currentRequest) setLoading(false);
    }
  }, [base, managementKey, ready]);

  useEffect(() => {
    if (!featureAvailability.checking) void load();
  }, [featureAvailability.checking, load]);

  useEffect(() => {
    if (!ready) return;
    const timer = window.setInterval(() => {
      void load();
    }, AUTO_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(timer);
  }, [load, ready]);

  useHeaderRefresh(load, ready);

  const handleSelect = async (name: string) => {
    if (!ready || pendingName) return;
    setPendingName(name);
    try {
      const next = await proxyNodesApi.select(base, name, managementKey);
      if (next) setOverview(next);
      else await load();
      showNotification(t('proxy_nodes.switch_success', { name }), 'success');
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      showNotification(`${t('proxy_nodes.switch_failed')}: ${message}`, 'error');
    } finally {
      setPendingName('');
    }
  };

  const handleHealthCheck = async () => {
    if (!ready || checking) return;
    setChecking(true);
    try {
      const result = await proxyNodesApi.healthCheck(base, managementKey);
      if (result.overview) {
        setOverview(result.overview);
      } else {
        setOverview((current) => {
          if (!current) return current;
          return {
            ...current,
            nodes: current.nodes.map((node) =>
              typeof result.delays[node.name] === 'number'
                ? { ...node, delay: result.delays[node.name] }
                : node
            ),
          };
        });
      }
      const failed = (result.providers ?? []).filter((item) => item.error);
      if (failed.length > 0) {
        showNotification(
          `${t('proxy_nodes.healthcheck_partial')}: ${failed.map((item) => item.name).join(', ')}`,
          'warning'
        );
      } else {
        showNotification(t('proxy_nodes.healthcheck_done'), 'success');
      }
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      showNotification(`${t('proxy_nodes.healthcheck_failed')}: ${message}`, 'error');
    } finally {
      setChecking(false);
    }
  };

  const handleDeleteNode = (node: ProxyNode) => {
    showConfirmation({
      title: t('proxy_nodes.delete_node_title'),
      message: t('proxy_nodes.delete_node_confirm', { name: node.name }),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await proxyNodesApi.removeNode(base, node.name, managementKey);
          showNotification(t('proxy_nodes.delete_node_success'), 'success');
          await load();
        } catch (err: unknown) {
          const message = err instanceof Error ? err.message : String(err);
          showNotification(`${t('proxy_nodes.delete_node_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const handleUpdateSource = async (source: ProxySource) => {
    if (!ready || busySource) return;
    setBusySource(source.name);
    try {
      await proxyNodesApi.updateSource(base, source.name, managementKey);
      showNotification(t('proxy_nodes.source_updated', { name: source.name }), 'success');
      await load();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      showNotification(`${t('proxy_nodes.source_update_failed')}: ${message}`, 'error');
    } finally {
      setBusySource('');
    }
  };

  const handleRemoveSource = (source: ProxySource) => {
    showConfirmation({
      title: t('proxy_nodes.source_delete_title'),
      message: t('proxy_nodes.source_delete_confirm', { name: source.name }),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: async () => {
        try {
          await proxyNodesApi.removeSource(base, source.name, managementKey);
          showNotification(t('proxy_nodes.source_deleted', { name: source.name }), 'success');
          await load();
        } catch (err: unknown) {
          const message = err instanceof Error ? err.message : String(err);
          showNotification(`${t('proxy_nodes.source_delete_failed')}: ${message}`, 'error');
        }
      },
    });
  };

  const nodes = overview?.nodes ?? [];
  const selectedName = overview?.group?.now ?? '';
  const autoGroup = overview?.autoGroup ?? null;
  const autoMode = Boolean(autoGroup && selectedName === autoGroup.name);
  const canEdit = Boolean(overview?.canEdit);
  const lastUpdated = useMemo(() => {
    if (!overview?.collectedAt) return '';
    return new Date(overview.collectedAt).toLocaleTimeString();
  }, [overview?.collectedAt]);

  if (featureAvailability.checking || (loading && !overview)) {
    return <LoadingSpinner />;
  }

  if (!ready) {
    return (
      <div className={styles.container}>
        <Card title={t('proxy_nodes.title')}>
          <div className={styles.notice}>{t('proxy_nodes.service_unavailable')}</div>
        </Card>
      </div>
    );
  }

  return (
    <div className={styles.container}>
      <div className={styles.content}>
        <Card
          title={t('proxy_nodes.title')}
          extra={
            <div className={styles.headerActions}>
              <Button
                variant="secondary"
                size="sm"
                onClick={handleHealthCheck}
                loading={checking}
                disabled={!overview?.available}
              >
                {t('proxy_nodes.healthcheck')}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => setImportOpen(true)}
                disabled={!canEdit}
              >
                <IconPlus size={14} />
                {t('proxy_nodes.import')}
              </Button>
              <Button variant="ghost" size="sm" onClick={() => void load()} loading={loading}>
                {t('common.refresh')}
              </Button>
            </div>
          }
        >
          <p className={styles.description}>{t('proxy_nodes.description')}</p>

          {error && <div className="error-box">{error}</div>}

          {overview && !overview.enabled && (
            <div className={styles.notice}>{t('proxy_nodes.disabled_hint')}</div>
          )}

          {overview?.enabled && !overview.available && (
            <div className={styles.noticeWarning}>
              <IconTriangleAlert size={16} />
              <span>{overview.error || t('proxy_nodes.unavailable')}</span>
            </div>
          )}

          {overview?.available && (
            <>
              <div className={styles.summary}>
                <div className={styles.summaryItem}>
                  <span className={styles.summaryLabel}>{t('proxy_nodes.current_egress')}</span>
                  <span className={styles.summaryValue}>
                    {autoMode ? t('proxy_nodes.auto_mode') : selectedName || '—'}
                  </span>
                </div>
                <div className={styles.summaryItem}>
                  <span className={styles.summaryLabel}>{t('proxy_nodes.auto_egress')}</span>
                  <span className={styles.summaryValue}>{autoGroup?.now || '—'}</span>
                </div>
                <div className={styles.summaryItem}>
                  <span className={styles.summaryLabel}>{t('proxy_nodes.node_count')}</span>
                  <span className={styles.summaryValue}>{nodes.length}</span>
                </div>
                <div className={styles.summaryItem}>
                  <span className={styles.summaryLabel}>{t('proxy_nodes.last_checked')}</span>
                  <span className={styles.summaryValue}>{lastUpdated || '—'}</span>
                </div>
              </div>

              <div className={styles.nodeGrid}>
                {autoGroup && (
                  <button
                    type="button"
                    className={`${styles.nodeCard} ${autoMode ? styles.nodeCardActive : ''}`}
                    onClick={() => void handleSelect(autoGroup.name)}
                    disabled={Boolean(pendingName)}
                    aria-pressed={autoMode}
                  >
                    <div className={styles.nodeHead}>
                      <span className={styles.nodeName}>{t('proxy_nodes.auto_mode')}</span>
                      {autoMode ? (
                        <span className={styles.selectedBadge}>
                          <IconCheck size={12} />
                          {t('proxy_nodes.in_use')}
                        </span>
                      ) : (
                        <IconRotateCcw size={14} />
                      )}
                    </div>
                    <div className={styles.nodeBadges}>
                      <span className={styles.badge}>{protocolLabel(autoGroup.type)}</span>
                      {autoGroup.now && <span className={styles.badgeMuted}>{autoGroup.now}</span>}
                    </div>
                  </button>
                )}

                {nodes.map((node) => (
                  <NodeCard
                    key={node.name}
                    node={node}
                    switching={pendingName === node.name}
                    disabled={Boolean(pendingName) || checking}
                    deletable={canEdit && node.provider === IMPORTED_PROVIDER}
                    onSelect={() => void handleSelect(node.name)}
                    onDelete={() => handleDeleteNode(node)}
                  />
                ))}
              </div>

              {nodes.length === 0 && <div className="hint">{t('proxy_nodes.empty')}</div>}

              <div className={styles.footer}>
                <IconSatellite size={14} />
                <span>
                  {t('proxy_nodes.test_url')}: {overview.testUrl}
                </span>
              </div>
            </>
          )}
        </Card>

        {canEdit && (
          <Card
            title={t('proxy_nodes.sources_title')}
            extra={
              <Button variant="secondary" size="sm" onClick={() => setImportOpen(true)}>
                <IconPlus size={14} />
                {t('proxy_nodes.import')}
              </Button>
            }
          >
            <p className={styles.description}>{t('proxy_nodes.sources_desc')}</p>
            {sources.length === 0 ? (
              <div className="hint">{t('proxy_nodes.sources_empty')}</div>
            ) : (
              <div className={styles.sourceList}>
                {sources.map((source) => (
                  <div key={source.name} className={styles.sourceRow}>
                    <div className={styles.sourceMeta}>
                      <div className={styles.sourceName}>
                        {source.name}
                        {source.builtin && (
                          <span className={styles.badgeMuted}>{t('proxy_nodes.builtin')}</span>
                        )}
                      </div>
                      <div className={styles.sourceSub}>
                        {source.type === 'file'
                          ? t('proxy_nodes.source_type_file')
                          : t('proxy_nodes.source_type_http')}
                        {source.url ? ` · ${source.url}` : ''}
                      </div>
                      <div className={styles.sourceSub}>
                        {t('proxy_nodes.source_nodes', { count: source.nodeCount })}
                        {source.intervalSeconds
                          ? ` · ${t('proxy_nodes.source_interval', {
                              minutes: Math.round(source.intervalSeconds / 60),
                            })}`
                          : ''}
                        {source.updatedAt && source.updatedAt.startsWith('0001')
                          ? ''
                          : source.updatedAt
                            ? ` · ${t('proxy_nodes.source_updated_at', { time: formatTime(source.updatedAt) })}`
                            : ''}
                      </div>
                    </div>
                    <div className={styles.sourceActions}>
                      {source.type !== 'file' && (
                        <Button
                          variant="secondary"
                          size="xs"
                          loading={busySource === source.name}
                          disabled={Boolean(busySource)}
                          onClick={() => void handleUpdateSource(source)}
                        >
                          {t('proxy_nodes.source_update')}
                        </Button>
                      )}
                      {!source.builtin && (
                        <Button
                          variant="ghost"
                          size="xs"
                          onClick={() => handleRemoveSource(source)}
                          disabled={Boolean(busySource)}
                        >
                          <IconTrash2 size={14} />
                        </Button>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Card>
        )}
      </div>

      <ImportDrawer
        open={importOpen}
        onClose={() => setImportOpen(false)}
        onImported={async () => {
          setImportOpen(false);
          await load();
        }}
        base={base}
        managementKey={managementKey}
      />
    </div>
  );
}

function NodeCard({
  node,
  switching,
  disabled,
  deletable,
  onSelect,
  onDelete,
}: {
  node: ProxyNode;
  switching: boolean;
  disabled: boolean;
  deletable: boolean;
  onSelect: () => void;
  onDelete: () => void;
}) {
  const { t } = useTranslation();
  const level = delayLevel(node.delay);

  return (
    <div
      className={`${styles.nodeCard} ${node.selected ? styles.nodeCardActive : ''}`}
      onClick={disabled ? undefined : onSelect}
      role="button"
      tabIndex={0}
      aria-pressed={node.selected}
      onKeyDown={(event) => {
        if (event.key === 'Enter' || event.key === ' ') {
          event.preventDefault();
          if (!disabled) onSelect();
        }
      }}
    >
      <div className={styles.nodeHead}>
        <span className={styles.nodeName} title={node.name}>
          {node.name}
        </span>
        <span className={`${styles.delay} ${styles[`delay_${level}`]}`}>
          {switching ? '…' : node.delay > 0 ? node.delay : '—'}
        </span>
      </div>
      <div className={styles.nodeBadges}>
        <span className={styles.badge}>{protocolLabel(node.type)}</span>
        {node.udp && <span className={styles.badge}>UDP</span>}
        {node.selected && (
          <span className={styles.badgeActive}>
            <IconCheck size={11} />
            {t('proxy_nodes.in_use')}
          </span>
        )}
        {!node.alive && (
          <span className={styles.badgeDanger}>{t('proxy_nodes.unhealthy')}</span>
        )}
      </div>
      {deletable && (
        <button
          type="button"
          className={styles.deleteButton}
          onClick={(event) => {
            event.stopPropagation();
            onDelete();
          }}
          title={t('proxy_nodes.delete_node_title')}
          aria-label={t('proxy_nodes.delete_node_title')}
        >
          <IconX size={12} />
        </button>
      )}
    </div>
  );
}

function ImportDrawer({
  open,
  onClose,
  onImported,
  base,
  managementKey,
}: {
  open: boolean;
  onClose: () => void;
  onImported: () => Promise<void> | void;
  base: string;
  managementKey?: string;
}) {
  const { t } = useTranslation();
  const { showNotification } = useNotificationStore();
  const [tab, setTab] = useState<ImportTab>('subscription');
  const [submitting, setSubmitting] = useState(false);
  const [subName, setSubName] = useState('');
  const [subURL, setSubURL] = useState('');
  const [subInterval, setSubInterval] = useState('60');
  const [links, setLinks] = useState('');
  const [yamlText, setYamlText] = useState('');

  useEffect(() => {
    if (!open) return;
    setSubmitting(false);
  }, [open]);

  const tabs: SegmentedTabItem<ImportTab>[] = [
    { id: 'subscription', label: t('proxy_nodes.tab_subscription') },
    { id: 'links', label: t('proxy_nodes.tab_links') },
    { id: 'yaml', label: t('proxy_nodes.tab_yaml') },
  ];

  const submit = async () => {
    if (submitting) return;
    setSubmitting(true);
    try {
      if (tab === 'subscription') {
        if (!subURL.trim()) {
          showNotification(t('proxy_nodes.subscription_url_required'), 'warning');
          return;
        }
        const minutes = Number.parseInt(subInterval, 10);
        await proxyNodesApi.addSubscription(
          base,
          {
            name: subName.trim() || undefined,
            url: subURL.trim(),
            intervalSeconds: Number.isFinite(minutes) && minutes > 0 ? minutes * 60 : undefined,
          },
          managementKey
        );
        setSubName('');
        setSubURL('');
        showNotification(t('proxy_nodes.subscription_added'), 'success');
      } else if (tab === 'links') {
        const parsed = links
          .split('\n')
          .map((line) => line.trim())
          .filter(Boolean);
        if (parsed.length === 0) {
          showNotification(t('proxy_nodes.links_required'), 'warning');
          return;
        }
        const result = await proxyNodesApi.importNodes(base, { links: parsed }, managementKey);
        setLinks('');
        showNotification(
          t('proxy_nodes.import_result', { added: result.added, total: result.total }),
          'success'
        );
      } else {
        if (!yamlText.trim()) {
          showNotification(t('proxy_nodes.yaml_required'), 'warning');
          return;
        }
        const result = await proxyNodesApi.importNodes(base, { yaml: yamlText }, managementKey);
        setYamlText('');
        showNotification(
          t('proxy_nodes.import_result', { added: result.added, total: result.total }),
          'success'
        );
      }
      await onImported();
    } catch (err: unknown) {
      const message = err instanceof Error ? err.message : String(err);
      showNotification(`${t('proxy_nodes.import_failed')}: ${message}`, 'error');
    } finally {
      setSubmitting(false);
    }
  };

  return (
    <Drawer
      open={open}
      onClose={onClose}
      title={t('proxy_nodes.import_title')}
      width={560}
      footer={
        <div className={styles.drawerFooter}>
          <Button variant="ghost" onClick={onClose} disabled={submitting}>
            {t('common.cancel')}
          </Button>
          <Button onClick={() => void submit()} loading={submitting}>
            {t('proxy_nodes.import_submit')}
          </Button>
        </div>
      }
    >
      <div className={styles.drawerBody}>
        <SegmentedTabs items={tabs} activeTab={tab} onChange={setTab} ariaLabel={t('proxy_nodes.import_title')} />

        {tab === 'subscription' && (
          <div className={styles.form}>
            <label className={styles.field} htmlFor="proxy-sub-name">
              <span className={styles.label}>{t('proxy_nodes.subscription_name')}</span>
              <Input
                id="proxy-sub-name"
                value={subName}
                placeholder={t('proxy_nodes.subscription_name_placeholder')}
                onChange={(event) => setSubName(event.target.value)}
              />
            </label>
            <label className={styles.field} htmlFor="proxy-sub-url">
              <span className={styles.label}>{t('proxy_nodes.subscription_url')}</span>
              <Input
                id="proxy-sub-url"
                value={subURL}
                placeholder="https://example.com/api/v1/client/subscribe?token=..."
                onChange={(event) => setSubURL(event.target.value)}
              />
            </label>
            <label className={styles.field} htmlFor="proxy-sub-interval">
              <span className={styles.label}>{t('proxy_nodes.subscription_interval')}</span>
              <Input
                id="proxy-sub-interval"
                value={subInterval}
                onChange={(event) => setSubInterval(event.target.value)}
              />
            </label>
            <div className={styles.hint}>{t('proxy_nodes.subscription_hint')}</div>
          </div>
        )}

        {tab === 'links' && (
          <div className={styles.form}>
            <label className={styles.field} htmlFor="proxy-links">
              <span className={styles.label}>{t('proxy_nodes.links_label')}</span>
              <textarea
                id="proxy-links"
                className={styles.textarea}
                rows={12}
                value={links}
                placeholder={'ss://...\nvmess://...\ntrojan://...\nhysteria2://...'}
                onChange={(event) => setLinks(event.target.value)}
              />
            </label>
            <div className={styles.hint}>{t('proxy_nodes.links_hint')}</div>
          </div>
        )}

        {tab === 'yaml' && (
          <div className={styles.form}>
            <label className={styles.field} htmlFor="proxy-yaml">
              <span className={styles.label}>{t('proxy_nodes.yaml_label')}</span>
              <textarea
                id="proxy-yaml"
                className={styles.textarea}
                rows={12}
                value={yamlText}
                placeholder={'proxies:\n  - name: HK-01\n    type: ss\n    server: ...'}
                onChange={(event) => setYamlText(event.target.value)}
              />
            </label>
            <div className={styles.hint}>{t('proxy_nodes.yaml_hint')}</div>
          </div>
        )}
      </div>
    </Drawer>
  );
}

function delayLevel(delay: number): 'fast' | 'ok' | 'slow' | 'unknown' {
  if (delay <= 0) return 'unknown';
  if (delay <= 250) return 'fast';
  if (delay <= 400) return 'ok';
  return 'slow';
}

function formatTime(value: string): string {
  const time = new Date(value);
  if (Number.isNaN(time.getTime()) || time.getTime() <= 0) return value;
  return time.toLocaleString();
}
