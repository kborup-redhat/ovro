import React from 'react';
import { render, screen, waitFor } from '@testing-library/react';
import '@testing-library/jest-dom';
import { OverviewPage } from '../components/OverviewPage';
import * as api from '../api/client';

jest.mock('../api/client');
const mockedApi = api as jest.Mocked<typeof api>;

describe('OverviewPage', () => {
  it('renders summary cards with data', async () => {
    mockedApi.getOverview.mockResolvedValue({
      totalVMs: 147,
      downsizeCandidates: 34,
      upsizeNeeded: 8,
      appliedToday: 5,
      totalCpuSavings: 86,
      totalMemorySavings: 219043332096,
    });

    render(<OverviewPage />);

    await waitFor(() => {
      expect(screen.getByText('147')).toBeInTheDocument();
      expect(screen.getByText('34')).toBeInTheDocument();
      expect(screen.getByText('8')).toBeInTheDocument();
      expect(screen.getByText('5')).toBeInTheDocument();
    });
  });

  it('renders loading state initially', () => {
    mockedApi.getOverview.mockReturnValue(new Promise(() => {}));
    render(<OverviewPage />);
    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });
});
