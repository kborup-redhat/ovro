import React from 'react';
import { render, screen } from '@testing-library/react';
import '@testing-library/jest-dom';
import { ExcludedVMsPage } from '../components/ExcludedVMsPage';

jest.mock('../api/client');

describe('ExcludedVMsPage', () => {
  it('renders empty state when no VMs are excluded', async () => {
    render(<ExcludedVMsPage />);
    expect(screen.getByText(/no excluded vms/i)).toBeInTheDocument();
  });
});
