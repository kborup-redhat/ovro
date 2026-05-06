import React, { useEffect, useState } from 'react';
import {
  Button,
  EmptyState,
  EmptyStateBody,
  PageSection,
  SearchInput,
  Spinner,
  Toolbar,
  ToolbarContent,
  ToolbarItem,
} from '@patternfly/react-core';
import { Table, Thead, Tr, Th, Tbody, Td } from '@patternfly/react-table';
import { removeExclusion } from '../api/client';

interface ExcludedVM {
  name: string;
  namespace: string;
  excludedSince: string;
}

export const ExcludedVMsPage: React.FC = () => {
  const [vms, setVMs] = useState<ExcludedVM[]>([]);
  const [loading, setLoading] = useState(true);
  const [search, setSearch] = useState('');

  useEffect(() => {
    // In a real implementation, this would use useK8sWatchResource from the console SDK
    // to watch VirtualMachines with the exclude annotation.
    // For now, we use a placeholder that completes loading.
    setLoading(false);
  }, []);

  const handleResume = async (namespace: string, name: string) => {
    try {
      await removeExclusion(namespace, name);
      setVMs((prev) => prev.filter((vm) => !(vm.name === name && vm.namespace === namespace)));
    } catch (e) {
      console.error('Failed to resume monitoring:', e);
    }
  };

  if (loading) {
    return <PageSection><Spinner /> Loading...</PageSection>;
  }

  const filtered = vms.filter((vm) =>
    vm.name.toLowerCase().includes(search.toLowerCase()),
  );

  if (vms.length === 0) {
    return (
      <PageSection>
        <EmptyState titleText="No excluded VMs" headingLevel="h2">
          <EmptyStateBody>No VMs are currently excluded from rightsizing monitoring.</EmptyStateBody>
        </EmptyState>
      </PageSection>
    );
  }

  return (
    <PageSection>
      <Toolbar>
        <ToolbarContent>
          <ToolbarItem>
            <SearchInput
              placeholder="Search excluded VMs..."
              value={search}
              onChange={(_e, v) => setSearch(v)}
              onClear={() => setSearch('')}
            />
          </ToolbarItem>
        </ToolbarContent>
      </Toolbar>
      <Table aria-label="Excluded VMs">
        <Thead>
          <Tr>
            <Th>VM Name</Th>
            <Th>Namespace</Th>
            <Th>Excluded Since</Th>
            <Th>Actions</Th>
          </Tr>
        </Thead>
        <Tbody>
          {filtered.map((vm) => (
            <Tr key={`${vm.namespace}/${vm.name}`}>
              <Td>{vm.name}</Td>
              <Td>{vm.namespace}</Td>
              <Td>{vm.excludedSince}</Td>
              <Td>
                <Button variant="secondary" size="sm" onClick={() => handleResume(vm.namespace, vm.name)}>
                  Resume Monitoring
                </Button>
              </Td>
            </Tr>
          ))}
        </Tbody>
      </Table>
      <p>{filtered.length} VMs excluded from monitoring</p>
    </PageSection>
  );
};
