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
} from '@patternfly/react-core';
import { CubesIcon } from '@patternfly/react-icons';
import { Table, Thead, Tr, Th, Tbody, Td } from '@patternfly/react-table';
import { VMListItem } from '../types';
import { listVMs, excludeVM, removeExclusion } from '../api/client';

export const AllVMsPage: React.FC = () => {
  const [vms, setVMs] = useState<VMListItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');

  const loadData = () => {
    setLoading(true);
    setError(null);
    listVMs()
      .then(setVMs)
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load VMs'))
      .finally(() => setLoading(false));
  };

  useEffect(() => { loadData(); }, []);

  const handleExclude = async (namespace: string, name: string) => {
    setError(null);
    try {
      await excludeVM(namespace, name);
      loadData();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to exclude VM');
    }
  };

  const handleResume = async (namespace: string, name: string) => {
    setError(null);
    try {
      await removeExclusion(namespace, name);
      loadData();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to resume monitoring');
    }
  };

  if (loading) {
    return (
      <Bullseye>
        <Spinner />
      </Bullseye>
    );
  }

  const filtered = vms.filter((vm) =>
    vm.name.toLowerCase().includes(search.toLowerCase()) ||
    vm.namespace.toLowerCase().includes(search.toLowerCase()),
  );

  if (vms.length === 0 && !error) {
    return (
      <EmptyState>
        <EmptyStateHeader titleText="No virtual machines" headingLevel="h2" icon={<EmptyStateIcon icon={CubesIcon} />} />
        <EmptyStateBody>No virtual machines found in accessible namespaces.</EmptyStateBody>
      </EmptyState>
    );
  }

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
      <Table aria-label="Virtual Machines">
        <Thead>
          <Tr>
            <Th>VM Name</Th>
            <Th>Namespace</Th>
            <Th>Status</Th>
            <Th>CPU</Th>
            <Th>Memory</Th>
            <Th>Rightsizing</Th>
            <Th>Actions</Th>
          </Tr>
        </Thead>
        <Tbody>
          {filtered.map((vm) => (
            <Tr key={`${vm.namespace}/${vm.name}`}>
              <Td>{vm.name}</Td>
              <Td>{vm.namespace}</Td>
              <Td>
                {vm.running
                  ? <Label color="green">Running</Label>
                  : <Label color="grey">Stopped</Label>}
              </Td>
              <Td>{vm.cpuCores} cores</Td>
              <Td>{vm.memory}</Td>
              <Td>
                {vm.excluded
                  ? <Label color="orange">Excluded</Label>
                  : <Label color="blue">Enabled</Label>}
              </Td>
              <Td>
                {vm.excluded ? (
                  <Button variant="secondary" size="sm" onClick={() => handleResume(vm.namespace, vm.name)}>
                    Resume Monitoring
                  </Button>
                ) : (
                  <Button variant="warning" size="sm" onClick={() => handleExclude(vm.namespace, vm.name)}>
                    Exclude
                  </Button>
                )}
              </Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
    </>
  );
};
