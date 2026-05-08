import React, { useEffect, useState } from 'react';
import {
  Alert,
  Bullseye,
  Button,
  EmptyState,
  EmptyStateBody,
  EmptyStateHeader,
  EmptyStateIcon,
  Label,
  SearchInput,
  Spinner,
  Toolbar,
  ToolbarContent,
  ToolbarItem,
  Tooltip,
} from '@patternfly/react-core';
import { SearchIcon } from '@patternfly/react-icons';
import { Table, Thead, Tr, Th, Tbody, Td } from '@patternfly/react-table';
import { RightsizingRecommendation } from '../types';
import { listRecommendations, revertRecommendation, rejectRecommendation } from '../api/client';

interface Props {
  onRightsize: (rec: RightsizingRecommendation) => void;
}

function formatReason(rec: RightsizingRecommendation): string {
  const p95 = rec.spec.metrics.cpuP95Percent.toFixed(1);
  const max = rec.spec.metrics.cpuMaxPercent.toFixed(1);
  if (rec.spec.direction === 'upsize') {
    if (Number(max) > Number(p95) + 10) {
      return `CPU spikes to ${max}% (sustained P95: ${p95}%)`;
    }
    return `CPU at ${p95}% sustained utilization`;
  }
  return `CPU only ${p95}% utilized, saves ${Math.abs(rec.spec.savings.cpu)} cores`;
}

export const RecommendationsPage: React.FC<Props> = ({ onRightsize }) => {
  const [recs, setRecs] = useState<RightsizingRecommendation[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');
  const [error, setError] = useState<string | null>(null);

  const loadData = () => {
    setLoading(true);
    setError(null);
    listRecommendations()
      .then(setRecs)
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load recommendations'))
      .finally(() => setLoading(false));
  };

  useEffect(() => { loadData(); }, []);

  const handleRevert = (rec: RightsizingRecommendation) => {
    setError(null);
    revertRecommendation(rec.metadata.namespace, rec.metadata.name)
      .then(() => loadData())
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to revert'));
  };

  const handleCancelApproval = (rec: RightsizingRecommendation) => {
    setError(null);
    rejectRecommendation(rec.metadata.namespace, rec.metadata.name, 'Cancelled by admin')
      .then(() => loadData())
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to cancel approval'));
  };

  if (loading) {
    return (
      <Bullseye>
        <Spinner />
      </Bullseye>
    );
  }

  const filtered = recs.filter((r) =>
    r.spec.virtualMachineRef.name.toLowerCase().includes(search.toLowerCase()),
  );

  if (filtered.length === 0 && !search && !error) {
    return (
      <EmptyState>
        <EmptyStateHeader titleText="No recommendations" headingLevel="h2" icon={<EmptyStateIcon icon={SearchIcon} />} />
        <EmptyStateBody>No rightsizing recommendations found. VMs will appear here once metrics have been collected.</EmptyStateBody>
      </EmptyState>
    );
  }

  const getActionButton = (rec: RightsizingRecommendation) => {
    switch (rec.status.state) {
      case 'pending':
        return <Button variant="primary" size="sm" onClick={() => onRightsize(rec)}>Rightsize</Button>;
      case 'applied-pending-restart':
        return (
          <Tooltip content="VM will restart at the scheduled time">
            <Button variant="warning" size="sm" isDisabled>Restart Pending</Button>
          </Tooltip>
        );
      case 'applied':
        return <Button variant="danger" size="sm" onClick={() => handleRevert(rec)}>Revert</Button>;
      case 'awaiting-approval':
        return (
          <Tooltip content={`Awaiting approval from ${rec.status.owner || 'owner'}`}>
            <Button variant="secondary" size="sm" onClick={() => handleCancelApproval(rec)}>
              Cancel Approval
            </Button>
          </Tooltip>
        );
      default:
        return null;
    }
  };

  const directionLabel = (dir: string) =>
    dir === 'downsize'
      ? <Label color="green">Downsize</Label>
      : <Label color="red">Upsize</Label>;

  const stateLabel = (state: string) => {
    const colors: Record<string, 'blue' | 'green' | 'orange' | 'red' | 'grey'> = {
      pending: 'blue',
      applied: 'green',
      'applied-pending-restart': 'orange',
      'awaiting-approval': 'orange',
      reverted: 'grey',
      failed: 'red',
    };
    return <Label color={colors[state] || 'grey'}>{state}</Label>;
  };

  return (
    <>
      {error && (
        <Alert variant="danger" title="Error" isInline style={{ marginBottom: '1rem' }}>
          {error}
        </Alert>
      )}
      <Toolbar>
        <ToolbarContent>
          <ToolbarItem>
            <SearchInput
              placeholder="Search VMs..."
              value={search}
              onChange={(_e, v) => setSearch(v)}
              onClear={() => setSearch('')}
            />
          </ToolbarItem>
        </ToolbarContent>
      </Toolbar>
      <Table aria-label="Recommendations">
        <Thead>
          <Tr>
            <Th>VM Name</Th>
            <Th>Namespace</Th>
            <Th>Direction</Th>
            <Th>CPU</Th>
            <Th>Memory</Th>
            <Th>Reason</Th>
            <Th>Hotplug</Th>
            <Th>Status</Th>
            <Th>Actions</Th>
          </Tr>
        </Thead>
        <Tbody>
          {filtered.map((rec) => (
            <Tr key={rec.metadata.name}>
              <Td>{rec.spec.virtualMachineRef.name}</Td>
              <Td>{rec.metadata.namespace}</Td>
              <Td>{directionLabel(rec.spec.direction)}</Td>
              <Td>{rec.spec.current.cpu.cores} &rarr; {rec.spec.recommended.cpu.cores}</Td>
              <Td>{rec.spec.current.memory} &rarr; {rec.spec.recommended.memory}</Td>
              <Td>{rec.spec.reason || formatReason(rec)}</Td>
              <Td>{rec.spec.hotplugCapable ? 'Yes' : 'No'}</Td>
              <Td>{stateLabel(rec.status.state)}</Td>
              <Td>{getActionButton(rec)}</Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
    </>
  );
};
