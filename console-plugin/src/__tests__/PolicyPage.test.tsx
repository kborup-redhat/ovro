import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';
import { PolicyPage } from '../components/PolicyPage';
import * as api from '../api/client';

jest.mock('../api/client');
const mockedApi = api as jest.Mocked<typeof api>;

describe('PolicyPage', () => {
  it('renders policy form with default values', async () => {
    mockedApi.getPolicy.mockResolvedValue({
      metadata: { name: 'default' },
      spec: {
        lookbackDays: 14,
        algorithm: { percentile: 95, headroomPercent: 20 },
        thresholds: { minCpuSavings: 1, minMemorySavings: '1Gi', upsizeUtilizationPercent: 90 },
        revertRetentionDays: 30,
        autoMode: { enabled: false, schedule: '0 2 * * *' },
        reconcileIntervalMinutes: 60,
      },
    });

    render(<PolicyPage />);

    await waitFor(() => {
      expect(screen.getByText(/save policy/i)).toBeInTheDocument();
    });
  });
});
