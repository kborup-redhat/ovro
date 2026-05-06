import React, { useState } from 'react';
import {
  Alert,
  Button,
  Modal,
  ModalVariant,
  Radio,
  Stack,
  StackItem,
  TextInput,
} from '@patternfly/react-core';
import { Table, Thead, Tr, Th, Tbody, Td } from '@patternfly/react-table';
import { RightsizingRecommendation, RestartOption } from '../types';
import { applyRecommendation } from '../api/client';

interface Props {
  recommendation: RightsizingRecommendation;
  isOpen: boolean;
  onClose: () => void;
  onApplied: () => void;
}

export const RightsizeDialog: React.FC<Props> = ({ recommendation, isOpen, onClose, onApplied }) => {
  const [restartOption, setRestartOption] = useState<RestartOption>('now');
  const [scheduledAt, setScheduledAt] = useState('');
  const [applying, setApplying] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [awaitingApproval, setAwaitingApproval] = useState(false);
  const [approvalOwner, setApprovalOwner] = useState('');

  const rec = recommendation;
  const isHotplug = rec.spec.hotplugCapable;
  const isPoweredOff = rec.spec.metrics.cpuP95Percent === 0 && rec.spec.metrics.memoryP95Percent === 0;

  const handleApply = async () => {
    setApplying(true);
    setError(null);
    try {
      const option = isPoweredOff ? 'later' : isHotplug ? 'now' : restartOption;
      const scheduledRFC3339 = restartOption === 'schedule' && scheduledAt
        ? new Date(scheduledAt).toISOString()
        : undefined;
      const result = await applyRecommendation(
        rec.metadata.namespace,
        rec.metadata.name,
        option,
        scheduledRFC3339,
      );

      if (result.awaitingApproval) {
        setAwaitingApproval(true);
        setApprovalOwner(result.owner || '');
      } else {
        onApplied();
        onClose();
      }
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Unknown error');
    } finally {
      setApplying(false);
    }
  };

  return (
    <Modal
      title={`Rightsize: ${rec.spec.virtualMachineRef.name}`}
      variant={ModalVariant.medium}
      isOpen={isOpen}
      onClose={onClose}
      actions={awaitingApproval ? [] : [
        <Button key="apply" variant="primary" onClick={handleApply} isLoading={applying} isDisabled={applying}>
          {isHotplug ? 'Apply Now (Live)' : 'Apply Changes'}
        </Button>,
        <Button key="cancel" variant="link" onClick={onClose}>Cancel</Button>,
      ]}
    >
      <Stack hasGutter>
        {awaitingApproval ? (
          <>
            <StackItem>
              <Alert variant="success" isInline title="Approval request sent">
                Notification sent to {approvalOwner}. The owner will review and approve the rightsizing changes.
              </Alert>
            </StackItem>
            <StackItem>
              <Button variant="primary" onClick={() => { onApplied(); onClose(); }}>Close</Button>
            </StackItem>
          </>
        ) : (
          <>
        {isPoweredOff ? (
          <StackItem>
            <Alert variant="info" isInline title="This VM appears to be powered off. Changes will take effect on next boot." />
          </StackItem>
        ) : isHotplug ? (
          <StackItem>
            <Alert variant="info" isInline title="This VM supports CPU/memory hotplug. Changes will be applied live without downtime." />
          </StackItem>
        ) : (
          <StackItem>
            <Alert variant="warning" isInline title="This VM does not support hotplug. A restart is required to apply changes." />
          </StackItem>
        )}

        <StackItem>
          <Table aria-label="Changes" variant="compact">
            <Thead>
              <Tr><Th>Resource</Th><Th>Current</Th><Th /><Th>New</Th><Th>Saving</Th></Tr>
            </Thead>
            <Tbody>
              <Tr>
                <Td>CPU Cores</Td>
                <Td>{rec.spec.current.cpu.cores}</Td>
                <Td>&rarr;</Td>
                <Td>{rec.spec.recommended.cpu.cores}</Td>
                <Td>{rec.spec.savings.cpu} cores</Td>
              </Tr>
              <Tr>
                <Td>Memory</Td>
                <Td>{rec.spec.current.memory}</Td>
                <Td>&rarr;</Td>
                <Td>{rec.spec.recommended.memory}</Td>
                <Td>{rec.spec.savings.memory}</Td>
              </Tr>
            </Tbody>
          </Table>
        </StackItem>

        <StackItem>
          <p>Based on {rec.spec.metrics.lookbackDays}-day analysis: CPU P95: {rec.spec.metrics.cpuP95Percent}%, Memory P95: {rec.spec.metrics.memoryP95Percent}%</p>
        </StackItem>

        {!isHotplug && !isPoweredOff && (
          <StackItem>
            <Stack hasGutter>
              <StackItem><strong>When should the VM restart?</strong></StackItem>
              <StackItem>
                <Radio id="restart-now" name="restart" label="Restart now" description="Apply changes and restart immediately" isChecked={restartOption === 'now'} onChange={() => setRestartOption('now')} />
              </StackItem>
              <StackItem>
                <Radio id="restart-schedule" name="restart" label="Schedule restart" description="Pick a date and time" isChecked={restartOption === 'schedule'} onChange={() => setRestartOption('schedule')} />
                {restartOption === 'schedule' && (
                  <TextInput type="datetime-local" value={scheduledAt} onChange={(_e, v) => setScheduledAt(v)} aria-label="Scheduled restart time" />
                )}
              </StackItem>
              <StackItem>
                <Radio id="restart-later" name="restart" label="Restart later" description="Apply spec changes now — restart manually during your change window" isChecked={restartOption === 'later'} onChange={() => setRestartOption('later')} />
              </StackItem>
            </Stack>
          </StackItem>
        )}

        <StackItem>
          <p>A revert option will be available for 30 days after applying.</p>
        </StackItem>

        {error && <StackItem><Alert variant="danger" isInline title={error} /></StackItem>}
          </>
        )}
      </Stack>
    </Modal>
  );
};
