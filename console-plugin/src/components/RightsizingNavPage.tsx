import React, { useState } from 'react';
import {
  PageSection,
  Tab,
  Tabs,
  TabTitleText,
  Title,
} from '@patternfly/react-core';
import { OverviewPage } from './OverviewPage';
import { RecommendationsPage } from './RecommendationsPage';
import { ExcludedVMsPage } from './ExcludedVMsPage';
import { PolicyPage } from './PolicyPage';
import { RightsizeDialog } from './RightsizeDialog';
import { RightsizingRecommendation } from '../types';

const RightsizingNavPage: React.FC = () => {
  const [activeTab, setActiveTab] = useState(0);
  const [dialogRec, setDialogRec] = useState<RightsizingRecommendation | null>(null);

  return (
    <>
      <PageSection>
        <Title headingLevel="h1">Rightsizing</Title>
      </PageSection>
      <PageSection type="tabs">
        <Tabs activeKey={activeTab} onSelect={(_e, k) => setActiveTab(k as number)}>
          <Tab eventKey={0} title={<TabTitleText>Overview</TabTitleText>}>
            <OverviewPage />
          </Tab>
          <Tab eventKey={1} title={<TabTitleText>Recommendations</TabTitleText>}>
            <RecommendationsPage onRightsize={setDialogRec} />
          </Tab>
          <Tab eventKey={2} title={<TabTitleText>Excluded VMs</TabTitleText>}>
            <ExcludedVMsPage />
          </Tab>
          <Tab eventKey={3} title={<TabTitleText>Policy</TabTitleText>}>
            <PolicyPage />
          </Tab>
        </Tabs>
      </PageSection>

      {dialogRec && (
        <RightsizeDialog
          recommendation={dialogRec}
          isOpen={!!dialogRec}
          onClose={() => setDialogRec(null)}
          onApplied={() => setDialogRec(null)}
        />
      )}
    </>
  );
};

export default RightsizingNavPage;
