/* eslint-disable */
import * as types from './graphql';
import type { TypedDocumentNode as DocumentNode } from '@graphql-typed-document-node/core';

/**
 * Map of all GraphQL operations in the project.
 *
 * This map has several performance disadvantages:
 * 1. It is not tree-shakeable, so it will include all operations in the project.
 * 2. It is not minifiable, so the string of a GraphQL query will be multiple times inside the bundle.
 * 3. It does not support dead code elimination, so it will add unused operations.
 *
 * Therefore it is highly recommended to use the babel or swc plugin for production.
 * Learn more about it here: https://the-guild.dev/graphql/codegen/plugins/presets/preset-client#reducing-bundle-size
 */
type Documents = {
    "\n  query PendingCount {\n    fleet {\n      pendingApprovals\n    }\n  }\n": typeof types.PendingCountDocument,
    "\n  mutation DecideApproval($id: ID!, $approve: Boolean!, $reason: String) {\n    decideApproval(id: $id, approve: $approve, reason: $reason) {\n      id\n      status\n    }\n  }\n": typeof types.DecideApprovalDocument,
    "\n  query Agent($name: String!) {\n    agent(name: $name) {\n      id\n      name\n      description\n      status\n      statusReason\n      provider\n      model\n      specYaml\n      reviewSince\n      triggers\n      tools {\n        name\n        description\n        kind\n        sideEffect\n        requiresApproval\n      }\n      review {\n        window\n        minRuns\n        onBreach\n      }\n      scorecard {\n        runs\n        kpis {\n          name\n          grader\n          aggregate\n          value\n          samples\n          target\n          status\n        }\n      }\n      comparison {\n        provider\n        model\n        shadow\n        runs\n        succeeded\n        avgCostUsd\n        avgSeconds\n        scorecard {\n          kpis {\n            name\n            grader\n            aggregate\n            value\n            samples\n            target\n            status\n          }\n        }\n      }\n      runs(limit: 20) {\n        id\n        status\n        trigger\n        model\n        costUsd\n        activeMs\n        startedAt\n        error\n      }\n      events(limit: 15) {\n        id\n        kind\n        actor\n        reason\n        createdAt\n      }\n    }\n  }\n": typeof types.AgentDocument,
    "\n  mutation PauseAgent($name: String!, $reason: String!) {\n    pauseAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n": typeof types.PauseAgentDocument,
    "\n  mutation ResumeAgent($name: String!, $reason: String) {\n    resumeAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n": typeof types.ResumeAgentDocument,
    "\n  mutation TriggerAgent($name: String!, $input: JSON) {\n    triggerAgent(name: $name, input: $input)\n  }\n": typeof types.TriggerAgentDocument,
    "\n  query Approvals {\n    approvals {\n      id\n      tool\n      input\n      status\n      createdAt\n      run {\n        id\n      }\n      agent {\n        name\n      }\n    }\n  }\n": typeof types.ApprovalsDocument,
    "\n  query Fleet {\n    fleet {\n      active\n      paused\n      retired\n      pendingApprovals\n      queueDepth\n      runs24h\n      costUsd24h\n    }\n    agents {\n      id\n      name\n      description\n      status\n      statusReason\n      model\n      stats {\n        runs\n        succeeded\n        failed\n        costUsd\n        lastRunAt\n      }\n      scorecard {\n        kpis {\n          name\n          status\n        }\n      }\n    }\n  }\n": typeof types.FleetDocument,
    "\n  query Run($id: ID!) {\n    run(id: $id) {\n      id\n      status\n      trigger\n      input\n      expected\n      output\n      error\n      provider\n      model\n      shadowOf\n      inputTokens\n      outputTokens\n      cacheReadTokens\n      costUsd\n      llmCalls\n      toolCalls\n      activeMs\n      startedAt\n      agent {\n        name\n      }\n      shadows {\n        id\n        status\n        model\n        costUsd\n      }\n      steps {\n        seq\n        kind\n        name\n        input\n        output\n        isError\n        simulated\n        inputTokens\n        outputTokens\n        cacheReadTokens\n        costUsd\n        latencyMs\n      }\n      approvals {\n        id\n        tool\n        input\n        status\n        decidedBy\n        reason\n        createdAt\n      }\n      scores {\n        kpi\n        grader\n        value\n        detail\n      }\n    }\n  }\n": typeof types.RunDocument,
};
const documents: Documents = {
    "\n  query PendingCount {\n    fleet {\n      pendingApprovals\n    }\n  }\n": types.PendingCountDocument,
    "\n  mutation DecideApproval($id: ID!, $approve: Boolean!, $reason: String) {\n    decideApproval(id: $id, approve: $approve, reason: $reason) {\n      id\n      status\n    }\n  }\n": types.DecideApprovalDocument,
    "\n  query Agent($name: String!) {\n    agent(name: $name) {\n      id\n      name\n      description\n      status\n      statusReason\n      provider\n      model\n      specYaml\n      reviewSince\n      triggers\n      tools {\n        name\n        description\n        kind\n        sideEffect\n        requiresApproval\n      }\n      review {\n        window\n        minRuns\n        onBreach\n      }\n      scorecard {\n        runs\n        kpis {\n          name\n          grader\n          aggregate\n          value\n          samples\n          target\n          status\n        }\n      }\n      comparison {\n        provider\n        model\n        shadow\n        runs\n        succeeded\n        avgCostUsd\n        avgSeconds\n        scorecard {\n          kpis {\n            name\n            grader\n            aggregate\n            value\n            samples\n            target\n            status\n          }\n        }\n      }\n      runs(limit: 20) {\n        id\n        status\n        trigger\n        model\n        costUsd\n        activeMs\n        startedAt\n        error\n      }\n      events(limit: 15) {\n        id\n        kind\n        actor\n        reason\n        createdAt\n      }\n    }\n  }\n": types.AgentDocument,
    "\n  mutation PauseAgent($name: String!, $reason: String!) {\n    pauseAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n": types.PauseAgentDocument,
    "\n  mutation ResumeAgent($name: String!, $reason: String) {\n    resumeAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n": types.ResumeAgentDocument,
    "\n  mutation TriggerAgent($name: String!, $input: JSON) {\n    triggerAgent(name: $name, input: $input)\n  }\n": types.TriggerAgentDocument,
    "\n  query Approvals {\n    approvals {\n      id\n      tool\n      input\n      status\n      createdAt\n      run {\n        id\n      }\n      agent {\n        name\n      }\n    }\n  }\n": types.ApprovalsDocument,
    "\n  query Fleet {\n    fleet {\n      active\n      paused\n      retired\n      pendingApprovals\n      queueDepth\n      runs24h\n      costUsd24h\n    }\n    agents {\n      id\n      name\n      description\n      status\n      statusReason\n      model\n      stats {\n        runs\n        succeeded\n        failed\n        costUsd\n        lastRunAt\n      }\n      scorecard {\n        kpis {\n          name\n          status\n        }\n      }\n    }\n  }\n": types.FleetDocument,
    "\n  query Run($id: ID!) {\n    run(id: $id) {\n      id\n      status\n      trigger\n      input\n      expected\n      output\n      error\n      provider\n      model\n      shadowOf\n      inputTokens\n      outputTokens\n      cacheReadTokens\n      costUsd\n      llmCalls\n      toolCalls\n      activeMs\n      startedAt\n      agent {\n        name\n      }\n      shadows {\n        id\n        status\n        model\n        costUsd\n      }\n      steps {\n        seq\n        kind\n        name\n        input\n        output\n        isError\n        simulated\n        inputTokens\n        outputTokens\n        cacheReadTokens\n        costUsd\n        latencyMs\n      }\n      approvals {\n        id\n        tool\n        input\n        status\n        decidedBy\n        reason\n        createdAt\n      }\n      scores {\n        kpi\n        grader\n        value\n        detail\n      }\n    }\n  }\n": types.RunDocument,
};

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 *
 *
 * @example
 * ```ts
 * const query = graphql(`query GetUser($id: ID!) { user(id: $id) { name } }`);
 * ```
 *
 * The query argument is unknown!
 * Please regenerate the types.
 */
export function graphql(source: string): unknown;

/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query PendingCount {\n    fleet {\n      pendingApprovals\n    }\n  }\n"): (typeof documents)["\n  query PendingCount {\n    fleet {\n      pendingApprovals\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation DecideApproval($id: ID!, $approve: Boolean!, $reason: String) {\n    decideApproval(id: $id, approve: $approve, reason: $reason) {\n      id\n      status\n    }\n  }\n"): (typeof documents)["\n  mutation DecideApproval($id: ID!, $approve: Boolean!, $reason: String) {\n    decideApproval(id: $id, approve: $approve, reason: $reason) {\n      id\n      status\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Agent($name: String!) {\n    agent(name: $name) {\n      id\n      name\n      description\n      status\n      statusReason\n      provider\n      model\n      specYaml\n      reviewSince\n      triggers\n      tools {\n        name\n        description\n        kind\n        sideEffect\n        requiresApproval\n      }\n      review {\n        window\n        minRuns\n        onBreach\n      }\n      scorecard {\n        runs\n        kpis {\n          name\n          grader\n          aggregate\n          value\n          samples\n          target\n          status\n        }\n      }\n      comparison {\n        provider\n        model\n        shadow\n        runs\n        succeeded\n        avgCostUsd\n        avgSeconds\n        scorecard {\n          kpis {\n            name\n            grader\n            aggregate\n            value\n            samples\n            target\n            status\n          }\n        }\n      }\n      runs(limit: 20) {\n        id\n        status\n        trigger\n        model\n        costUsd\n        activeMs\n        startedAt\n        error\n      }\n      events(limit: 15) {\n        id\n        kind\n        actor\n        reason\n        createdAt\n      }\n    }\n  }\n"): (typeof documents)["\n  query Agent($name: String!) {\n    agent(name: $name) {\n      id\n      name\n      description\n      status\n      statusReason\n      provider\n      model\n      specYaml\n      reviewSince\n      triggers\n      tools {\n        name\n        description\n        kind\n        sideEffect\n        requiresApproval\n      }\n      review {\n        window\n        minRuns\n        onBreach\n      }\n      scorecard {\n        runs\n        kpis {\n          name\n          grader\n          aggregate\n          value\n          samples\n          target\n          status\n        }\n      }\n      comparison {\n        provider\n        model\n        shadow\n        runs\n        succeeded\n        avgCostUsd\n        avgSeconds\n        scorecard {\n          kpis {\n            name\n            grader\n            aggregate\n            value\n            samples\n            target\n            status\n          }\n        }\n      }\n      runs(limit: 20) {\n        id\n        status\n        trigger\n        model\n        costUsd\n        activeMs\n        startedAt\n        error\n      }\n      events(limit: 15) {\n        id\n        kind\n        actor\n        reason\n        createdAt\n      }\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation PauseAgent($name: String!, $reason: String!) {\n    pauseAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n"): (typeof documents)["\n  mutation PauseAgent($name: String!, $reason: String!) {\n    pauseAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation ResumeAgent($name: String!, $reason: String) {\n    resumeAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n"): (typeof documents)["\n  mutation ResumeAgent($name: String!, $reason: String) {\n    resumeAgent(name: $name, reason: $reason) {\n      id\n      status\n      statusReason\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  mutation TriggerAgent($name: String!, $input: JSON) {\n    triggerAgent(name: $name, input: $input)\n  }\n"): (typeof documents)["\n  mutation TriggerAgent($name: String!, $input: JSON) {\n    triggerAgent(name: $name, input: $input)\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Approvals {\n    approvals {\n      id\n      tool\n      input\n      status\n      createdAt\n      run {\n        id\n      }\n      agent {\n        name\n      }\n    }\n  }\n"): (typeof documents)["\n  query Approvals {\n    approvals {\n      id\n      tool\n      input\n      status\n      createdAt\n      run {\n        id\n      }\n      agent {\n        name\n      }\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Fleet {\n    fleet {\n      active\n      paused\n      retired\n      pendingApprovals\n      queueDepth\n      runs24h\n      costUsd24h\n    }\n    agents {\n      id\n      name\n      description\n      status\n      statusReason\n      model\n      stats {\n        runs\n        succeeded\n        failed\n        costUsd\n        lastRunAt\n      }\n      scorecard {\n        kpis {\n          name\n          status\n        }\n      }\n    }\n  }\n"): (typeof documents)["\n  query Fleet {\n    fleet {\n      active\n      paused\n      retired\n      pendingApprovals\n      queueDepth\n      runs24h\n      costUsd24h\n    }\n    agents {\n      id\n      name\n      description\n      status\n      statusReason\n      model\n      stats {\n        runs\n        succeeded\n        failed\n        costUsd\n        lastRunAt\n      }\n      scorecard {\n        kpis {\n          name\n          status\n        }\n      }\n    }\n  }\n"];
/**
 * The graphql function is used to parse GraphQL queries into a document that can be used by GraphQL clients.
 */
export function graphql(source: "\n  query Run($id: ID!) {\n    run(id: $id) {\n      id\n      status\n      trigger\n      input\n      expected\n      output\n      error\n      provider\n      model\n      shadowOf\n      inputTokens\n      outputTokens\n      cacheReadTokens\n      costUsd\n      llmCalls\n      toolCalls\n      activeMs\n      startedAt\n      agent {\n        name\n      }\n      shadows {\n        id\n        status\n        model\n        costUsd\n      }\n      steps {\n        seq\n        kind\n        name\n        input\n        output\n        isError\n        simulated\n        inputTokens\n        outputTokens\n        cacheReadTokens\n        costUsd\n        latencyMs\n      }\n      approvals {\n        id\n        tool\n        input\n        status\n        decidedBy\n        reason\n        createdAt\n      }\n      scores {\n        kpi\n        grader\n        value\n        detail\n      }\n    }\n  }\n"): (typeof documents)["\n  query Run($id: ID!) {\n    run(id: $id) {\n      id\n      status\n      trigger\n      input\n      expected\n      output\n      error\n      provider\n      model\n      shadowOf\n      inputTokens\n      outputTokens\n      cacheReadTokens\n      costUsd\n      llmCalls\n      toolCalls\n      activeMs\n      startedAt\n      agent {\n        name\n      }\n      shadows {\n        id\n        status\n        model\n        costUsd\n      }\n      steps {\n        seq\n        kind\n        name\n        input\n        output\n        isError\n        simulated\n        inputTokens\n        outputTokens\n        cacheReadTokens\n        costUsd\n        latencyMs\n      }\n      approvals {\n        id\n        tool\n        input\n        status\n        decidedBy\n        reason\n        createdAt\n      }\n      scores {\n        kpi\n        grader\n        value\n        detail\n      }\n    }\n  }\n"];

export function graphql(source: string) {
  return (documents as any)[source] ?? {};
}

export type DocumentType<TDocumentNode extends DocumentNode<any, any>> = TDocumentNode extends DocumentNode<  infer TType,  any>  ? TType  : never;