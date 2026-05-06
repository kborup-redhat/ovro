import React, { useEffect, useState } from 'react';
import {
  Alert,
  Bullseye,
  Card,
  CardBody,
  CardHeader,
  CardTitle,
  Gallery,
  GalleryItem,
  Spinner,
  Title,
} from '@patternfly/react-core';
import { OverviewData } from '../types';
import { getOverview } from '../api/client';

export const OverviewPage: React.FC = () => {
  const [data, setData] = useState<OverviewData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getOverview()
      .then(setData)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false));
  }, []);

  if (loading) {
    return (
      <Bullseye>
        <Spinner />
      </Bullseye>
    );
  }

  if (error || !data) {
    return (
      <Alert variant="danger" isInline title="Error loading overview">
        {error}
      </Alert>
    );
  }

  const memorySavingsGiB = Math.round(data.totalMemorySavings / (1024 * 1024 * 1024));

  return (
    <Gallery hasGutter minWidths={{ default: '250px' }}>
      <GalleryItem>
        <Card isFullHeight>
          <CardHeader>
            <CardTitle>Total VMs Monitored</CardTitle>
          </CardHeader>
          <CardBody>
            <Title headingLevel="h2" size="3xl">{data.totalVMs}</Title>
          </CardBody>
        </Card>
      </GalleryItem>
      <GalleryItem>
        <Card isFullHeight>
          <CardHeader>
            <CardTitle>Downsize Candidates</CardTitle>
          </CardHeader>
          <CardBody>
            <Title headingLevel="h2" size="3xl">{data.downsizeCandidates}</Title>
            <p>saving {data.totalCpuSavings} CPUs, {memorySavingsGiB} GiB</p>
          </CardBody>
        </Card>
      </GalleryItem>
      <GalleryItem>
        <Card isFullHeight>
          <CardHeader>
            <CardTitle>Upsize Needed</CardTitle>
          </CardHeader>
          <CardBody>
            <Title headingLevel="h2" size="3xl">{data.upsizeNeeded}</Title>
            <p>VMs above 90% utilization</p>
          </CardBody>
        </Card>
      </GalleryItem>
      <GalleryItem>
        <Card isFullHeight>
          <CardHeader>
            <CardTitle>Applied Today</CardTitle>
          </CardHeader>
          <CardBody>
            <Title headingLevel="h2" size="3xl">{data.appliedToday}</Title>
          </CardBody>
        </Card>
      </GalleryItem>
    </Gallery>
  );
};
