import React from 'react';
import { render, screen } from '@testing-library/react';
import '@testing-library/jest-dom';
import { RightsizeDialog } from '../components/RightsizeDialog';
import * as api from '../api/client';
import { RightsizingRecommendation } from '../types';

jest.mock('../api/client');

const hotplugRec: RightsizingRecommendation = {
  metadata: { name: 'vm-test', namespace: 'default', creationTimestamp: '' },
  spec: {
    virtualMachineRef: { name: 'test', namespace: 'default' },
    direction: 'downsize',
    current: { cpu: { cores: 8, sockets: 1, threads: 1 }, memory: '16Gi' },
    recommended: { cpu: { cores: 4, sockets: 1, threads: 1 }, memory: '8Gi' },
    savings: { cpu: 4, memory: '8Gi' },
    metrics: { lookbackDays: 14, cpuP95Percent: 28.3, memoryP95Percent: 41.7, cpuMaxPercent: 62.1, memoryMaxPercent: 58.4 },
    hotplugCapable: true,
  },
  status: { state: 'pending', lastCalculated: null, appliedAt: null, scheduledRestartAt: null, revertBefore: null, revertConfig: null, message: '' },
};

const noHotplugRec: RightsizingRecommendation = {
  ...hotplugRec,
  spec: { ...hotplugRec.spec, hotplugCapable: false },
};

describe('RightsizeDialog', () => {
  it('shows Apply Now (Live) button for hotplug VMs', () => {
    render(<RightsizeDialog recommendation={hotplugRec} isOpen onClose={() => {}} onApplied={() => {}} />);
    expect(screen.getByText(/apply now/i)).toBeInTheDocument();
  });

  it('shows restart options for non-hotplug VMs', () => {
    render(<RightsizeDialog recommendation={noHotplugRec} isOpen onClose={() => {}} onApplied={() => {}} />);
    expect(screen.getByText(/restart now/i)).toBeInTheDocument();
    expect(screen.getByText(/schedule restart/i)).toBeInTheDocument();
    expect(screen.getByText(/restart later/i)).toBeInTheDocument();
  });

  it('shows current vs recommended values', () => {
    render(<RightsizeDialog recommendation={hotplugRec} isOpen onClose={() => {}} onApplied={() => {}} />);
    expect(screen.getByText('8')).toBeInTheDocument();
    expect(screen.getByText('4')).toBeInTheDocument();
  });
});
