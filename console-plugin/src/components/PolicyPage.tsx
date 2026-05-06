import React, { useEffect, useState } from 'react';
import {
  ActionGroup,
  Alert,
  Bullseye,
  Button,
  Form,
  FormGroup,
  FormSection,
  Spinner,
  Switch,
  TextInput,
} from '@patternfly/react-core';
import { RightsizingPolicySpec } from '../types';
import { getPolicy, updatePolicy } from '../api/client';

const defaultSpec: RightsizingPolicySpec = {
  lookbackDays: 14,
  algorithm: { percentile: 95, headroomPercent: 20 },
  thresholds: { minCpuSavings: 1, minMemorySavings: '1Gi', upsizeUtilizationPercent: 90 },
  revertRetentionDays: 30,
  autoMode: { enabled: false, schedule: '0 2 * * *' },
  reconcileIntervalMinutes: 60,
};

export const PolicyPage: React.FC = () => {
  const [spec, setSpec] = useState<RightsizingPolicySpec>(defaultSpec);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState(false);

  useEffect(() => {
    getPolicy()
      .then((p) => setSpec(p.spec))
      .catch(() => setSpec(defaultSpec))
      .finally(() => setLoading(false));
  }, []);

  const handleSave = async () => {
    setSaving(true);
    setError(null);
    setSuccess(false);
    try {
      await updatePolicy(spec);
      setSuccess(true);
    } catch (e: unknown) {
      setError(e instanceof Error ? e.message : 'Failed to save policy');
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <Bullseye>
        <Spinner />
      </Bullseye>
    );
  }

  return (
    <Form style={{ maxWidth: '800px' }}>
      <FormSection title="Analysis Settings">
        <FormGroup label="Lookback Period (days)" fieldId="lookback">
          <TextInput id="lookback" type="number" value={spec.lookbackDays} onChange={(_e, v) => setSpec({ ...spec, lookbackDays: parseInt(v) || 0 })} />
        </FormGroup>
        <FormGroup label="Percentile for Calculation" fieldId="percentile">
          <TextInput id="percentile" type="number" value={spec.algorithm.percentile} onChange={(_e, v) => setSpec({ ...spec, algorithm: { ...spec.algorithm, percentile: parseInt(v) || 0 } })} />
        </FormGroup>
        <FormGroup label="Headroom (%)" fieldId="headroom">
          <TextInput id="headroom" type="number" value={spec.algorithm.headroomPercent} onChange={(_e, v) => setSpec({ ...spec, algorithm: { ...spec.algorithm, headroomPercent: parseInt(v) || 0 } })} />
        </FormGroup>
        <FormGroup label="Reconcile Interval (minutes)" fieldId="interval">
          <TextInput id="interval" type="number" value={spec.reconcileIntervalMinutes} onChange={(_e, v) => setSpec({ ...spec, reconcileIntervalMinutes: parseInt(v) || 0 })} />
        </FormGroup>
      </FormSection>

      <FormSection title="Thresholds">
        <FormGroup label="Min CPU Savings (cores)" fieldId="minCpu">
          <TextInput id="minCpu" type="number" value={spec.thresholds.minCpuSavings} onChange={(_e, v) => setSpec({ ...spec, thresholds: { ...spec.thresholds, minCpuSavings: parseInt(v) || 0 } })} />
        </FormGroup>
        <FormGroup label="Min Memory Savings (GiB)" fieldId="minMem">
          <TextInput id="minMem" value={spec.thresholds.minMemorySavings} onChange={(_e, v) => setSpec({ ...spec, thresholds: { ...spec.thresholds, minMemorySavings: v } })} />
        </FormGroup>
        <FormGroup label="Upsize Utilization Threshold (%)" fieldId="upsizePct">
          <TextInput id="upsizePct" type="number" value={spec.thresholds.upsizeUtilizationPercent} onChange={(_e, v) => setSpec({ ...spec, thresholds: { ...spec.thresholds, upsizeUtilizationPercent: parseInt(v) || 0 } })} />
        </FormGroup>
      </FormSection>

      <FormSection title="Revert Settings">
        <FormGroup label="Revert Retention (days)" fieldId="revertDays">
          <TextInput id="revertDays" type="number" value={spec.revertRetentionDays} onChange={(_e, v) => setSpec({ ...spec, revertRetentionDays: parseInt(v) || 0 })} />
        </FormGroup>
      </FormSection>

      <FormSection title="Auto Mode">
        <FormGroup fieldId="autoEnabled">
          <Switch id="autoEnabled" label="Enable Auto Rightsizing" isChecked={spec.autoMode.enabled} onChange={(_e, v) => setSpec({ ...spec, autoMode: { ...spec.autoMode, enabled: v } })} />
        </FormGroup>
        <FormGroup label="Schedule (cron)" fieldId="schedule">
          <TextInput id="schedule" value={spec.autoMode.schedule} onChange={(_e, v) => setSpec({ ...spec, autoMode: { ...spec.autoMode, schedule: v } })} isDisabled={!spec.autoMode.enabled} />
        </FormGroup>
        {spec.autoMode.enabled && (
          <Alert variant="info" isInline isPlain title="VMs with an owner label will require owner approval before changes are applied." />
        )}
      </FormSection>

      {error && <Alert variant="danger" isInline title={error} />}
      {success && <Alert variant="success" isInline title="Policy saved successfully" />}

      <ActionGroup>
        <Button variant="primary" onClick={handleSave} isLoading={saving} isDisabled={saving}>Save Policy</Button>
        <Button variant="secondary" onClick={() => setSpec(defaultSpec)}>Reset to Defaults</Button>
      </ActionGroup>
    </Form>
  );
};
