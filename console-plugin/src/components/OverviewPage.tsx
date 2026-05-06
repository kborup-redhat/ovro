import React, { useEffect, useState } from 'react';
import {
  Card,
  CardBody,
  CardTitle,
  PageSection,
  Spinner,
  Title,
} from '@patternfly/react-core';
import { Grid, GridItem } from '@patternfly/react-core';
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
      <PageSection>
        <Spinner /> Loading...
      </PageSection>
    );
  }

  if (error || !data) {
    return <PageSection>Error loading overview: {error}</PageSection>;
  }

  const memorySavingsGiB = Math.round(data.totalMemorySavings / (1024 * 1024 * 1024));

  return (
    <PageSection>
      <Grid hasGutter>
        <GridItem span={3}>
          <Card>
            <CardTitle>Total VMs Monitored</CardTitle>
            <CardBody>
              <Title headingLevel="h2" size="3xl">{data.totalVMs}</Title>
            </CardBody>
          </Card>
        </GridItem>
        <GridItem span={3}>
          <Card>
            <CardTitle>Downsize Candidates</CardTitle>
            <CardBody>
              <Title headingLevel="h2" size="3xl">{data.downsizeCandidates}</Title>
              <p>saving {data.totalCpuSavings} CPUs, {memorySavingsGiB} GiB</p>
            </CardBody>
          </Card>
        </GridItem>
        <GridItem span={3}>
          <Card>
            <CardTitle>Upsize Needed</CardTitle>
            <CardBody>
              <Title headingLevel="h2" size="3xl">{data.upsizeNeeded}</Title>
              <p>VMs above 90% utilization</p>
            </CardBody>
          </Card>
        </GridItem>
        <GridItem span={3}>
          <Card>
            <CardTitle>Applied Today</CardTitle>
            <CardBody>
              <Title headingLevel="h2" size="3xl">{data.appliedToday}</Title>
            </CardBody>
          </Card>
        </GridItem>
      </Grid>
    </PageSection>
  );
};
