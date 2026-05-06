import React, { useEffect, useState } from 'react';
import {
  Alert,
  Button,
  EmptyState,
  EmptyStateBody,
  Label,
  PageSection,
  SearchInput,
  Spinner,
  Toolbar,
  ToolbarContent,
  ToolbarItem,
  Tooltip,
} from '@patternfly/react-core';
import { Table, Thead, Tr, Th, Tbody, Td } from '@patternfly/react-table';
import { RightsizingRecommendation } from '../types';
import { listRecommendations, revertRecommendation } from '../api/client';

interface Props {
  onRightsize: (rec: RightsizingRecommendation) => void;
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

  if (loading) {
    return <PageSection><Spinner /> Loading...</PageSection>;
  }

  const filtered = recs.filter((r) =>
    r.spec.virtualMachineRef.name.toLowerCase().includes(search.toLowerCase()),
  );

  if (filtered.length === 0 && !search && !error) {
    return (
      <PageSection>
        <EmptyState titleText="No recommendations" headingLevel="h2">
          <EmptyStateBody>No rightsizing recommendations found.</EmptyStateBody>
        </EmptyState>
      </PageSection>
    );
  }

  const getActionButton = (rec: RightsizingRecommendation) => {
    switch (rec.status.state) {
      case 'pending':
        return <Button variant="primary" size="sm" onClick={() => onRightsize(rec)}>Rightsize</Button>;
      case 'applied-pending-restart':
        return (
          <Tooltip content="VM will restart at the scheduled time">
            <Button variant="warning" size="sm" isDisabled>Restart</Button>
          </Tooltip>
        );
      case 'applied':
        return <Button variant="danger" size="sm" onClick={() => handleRevert(rec)}>Revert</Button>;
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
      reverted: 'grey',
      failed: 'red',
    };
    return <Label color={colors[state] || 'grey'}>{state}</Label>;
  };

  return (
    <PageSection>
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
            <Th>P95 CPU</Th>
            <Th>P95 Mem</Th>
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
              <Td>{rec.spec.current.cpu.cores} → {rec.spec.recommended.cpu.cores}</Td>
              <Td>{rec.spec.current.memory} → {rec.spec.recommended.memory}</Td>
              <Td>{rec.spec.metrics.cpuP95Percent}%</Td>
              <Td>{rec.spec.metrics.memoryP95Percent}%</Td>
              <Td>{rec.spec.hotplugCapable ? 'Yes' : 'No'}</Td>
              <Td>{stateLabel(rec.status.state)}</Td>
              <Td>{getActionButton(rec)}</Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
    </PageSection>
  );
};
