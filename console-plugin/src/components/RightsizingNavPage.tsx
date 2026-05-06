import React, { useState } from 'react';
import {
  Page,
  PageSection,
  PageSectionVariants,
  Tab,
  Tabs,
  TabTitleText,
  Title,
} from '@patternfly/react-core';
import { OverviewPage } from './OverviewPage';
import { RecommendationsPage } from './RecommendationsPage';
import { AllVMsPage } from './AllVMsPage';
import { ExcludedVMsPage } from './ExcludedVMsPage';
import { PolicyPage } from './PolicyPage';
import { RightsizeDialog } from './RightsizeDialog';
import { RightsizingRecommendation } from '../types';

const RightsizingNavPage: React.FC = () => {
  const [activeTab, setActiveTab] = useState(0);
  const [dialogRec, setDialogRec] = useState<RightsizingRecommendation | null>(null);
  const [refreshKey, setRefreshKey] = useState(0);

  return (
    <Page>
      <PageSection variant={PageSectionVariants.light}>
        <Title headingLevel="h1" size="lg">Rightsizing</Title>
      </PageSection>
      <PageSection variant={PageSectionVariants.light} type="tabs" hasShadowBottom>
        <Tabs activeKey={activeTab} onSelect={(_e, k) => setActiveTab(k as number)}>
          <Tab eventKey={0} title={<TabTitleText>Overview</TabTitleText>} />
          <Tab eventKey={1} title={<TabTitleText>Recommendations</TabTitleText>} />
          <Tab eventKey={2} title={<TabTitleText>Virtual Machines</TabTitleText>} />
          <Tab eventKey={3} title={<TabTitleText>Excluded VMs</TabTitleText>} />
          <Tab eventKey={4} title={<TabTitleText>Policy</TabTitleText>} />
        </Tabs>
      </PageSection>
      <PageSection variant={PageSectionVariants.default} isFilled>
        {activeTab === 0 && <OverviewPage />}
        {activeTab === 1 && <RecommendationsPage key={refreshKey} onRightsize={setDialogRec} />}
        {activeTab === 2 && <AllVMsPage />}
        {activeTab === 3 && <ExcludedVMsPage />}
        {activeTab === 4 && <PolicyPage />}
      </PageSection>

      {dialogRec && (
        <RightsizeDialog
          recommendation={dialogRec}
          isOpen={!!dialogRec}
          onClose={() => setDialogRec(null)}
          onApplied={() => { setDialogRec(null); setRefreshKey(k => k + 1); }}
        />
      )}
    </Page>
  );
};

export default RightsizingNavPage;
