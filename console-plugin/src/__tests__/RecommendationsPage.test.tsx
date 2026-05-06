import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';
import { RecommendationsPage } from '../components/RecommendationsPage';
import * as api from '../api/client';
import { RightsizingRecommendation } from '../types';

jest.mock('../api/client');
const mockedApi = api as jest.Mocked<typeof api>;

const mockRec: RightsizingRecommendation = {
  metadata: { name: 'vm-webserver-01', namespace: 'production', creationTimestamp: '2026-05-06T10:00:00Z' },
  spec: {
    virtualMachineRef: { name: 'webserver-01', namespace: 'production' },
    direction: 'downsize',
    current: { cpu: { cores: 8, sockets: 1, threads: 1 }, memory: '16Gi' },
    recommended: { cpu: { cores: 4, sockets: 1, threads: 1 }, memory: '8Gi' },
    savings: { cpu: 4, memory: '8Gi' },
    metrics: { lookbackDays: 14, cpuP95Percent: 28.3, memoryP95Percent: 41.7, cpuMaxPercent: 62.1, memoryMaxPercent: 58.4 },
    hotplugCapable: true,
  },
  status: { state: 'pending', lastCalculated: '2026-05-06T10:30:00Z', appliedAt: null, scheduledRestartAt: null, revertBefore: null, revertConfig: null, message: '' },
};

describe('RecommendationsPage', () => {
  it('renders recommendations table with data', async () => {
    mockedApi.listRecommendations.mockResolvedValue([mockRec]);
    render(<RecommendationsPage onRightsize={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText('webserver-01')).toBeInTheDocument();
      expect(screen.getByText('production')).toBeInTheDocument();
    });
  });

  it('shows Rightsize button for pending recommendations', async () => {
    mockedApi.listRecommendations.mockResolvedValue([mockRec]);
    render(<RecommendationsPage onRightsize={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText('Rightsize')).toBeInTheDocument();
    });
  });

  it('renders empty state when no recommendations', async () => {
    mockedApi.listRecommendations.mockResolvedValue([]);
    render(<RecommendationsPage onRightsize={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText(/no recommendations/i)).toBeInTheDocument();
    });
  });
});
