#!/usr/bin/env node
// CI guard: production code PRs must include a non-empty changelog block in
// their description. Opt-out: apply the "skip-changelog" label to the PR.
//
// Expects the workflow to provide:
//   PR_BODY       — the PR description (github.event.pull_request.body)
//   PR_LABELS     — comma-separated label names
//   TARGET_BRANCH — the PR base branch (github.base_ref), defaults to master

import { execSync } from 'node:child_process';
import {
  START_MARKER,
  END_MARKER,
  ALLOWED_SECTIONS,
  extractBlock,
  parseBlock,
  bulletCount,
} from './changelog-block.mjs';

const env = process.env;

function fail(message) {
  console.error(`ERROR: ${message}`);
  process.exit(1);
}

const labels = (env.PR_LABELS || '')
  .split(',')
  .map((label) => label.trim())
  .filter(Boolean);

if (labels.includes('skip-changelog')) {
  console.log('skip-changelog label present on PR; skipping check');
  process.exit(0);
}

const target = env.TARGET_BRANCH || 'master';
try {
  execSync(`git fetch --depth=200 origin ${target}`, { stdio: 'ignore' });
} catch {
  // Best-effort. The diff below will still work against whatever the runner
  // already has fetched.
}

const changed = execSync(`git diff --name-only origin/${target}...HEAD`, {
  encoding: 'utf8',
})
  .split('\n')
  .filter(Boolean);

// Production = Go sources, excluding tests, test helpers, and vendor.
// Touching the Makefile, docs, or CI is not "production" for this guard.
const isProduction = (path) => {
  if (!/\.go$/.test(path)) return false;
  if (/_test\.go$/.test(path)) return false;
  if (/^vendor\//.test(path)) return false;
  if (/^db\/dbtest\//.test(path)) return false;
  return true;
};

const productionChanges = changed.filter(isProduction);
if (productionChanges.length === 0) {
  console.log('no production code changes; changelog block not required');
  process.exit(0);
}

const block = extractBlock(env.PR_BODY || '');
if (block === null) {
  fail(
    `this PR changes production code (${productionChanges.length} file(s)) but its description has no ` +
      `changelog block (${START_MARKER} … ${END_MARKER}). ` +
      'Fill in the "## Changelog" section from the PR template, or apply the skip-changelog label.',
  );
}

const { sections, placeholders, unknownSections } = parseBlock(block);

if (placeholders.length > 0) {
  fail(
    `the changelog block still contains the template placeholder bullet:\n  ${placeholders.join('\n  ')}\n` +
      'Replace it with a one-line summary of the user-visible change.',
  );
}

if (unknownSections.length > 0) {
  fail(
    `the changelog block contains unknown section heading(s): ${unknownSections.join(', ')}. ` +
      `Use one of: ${ALLOWED_SECTIONS.map((name) => `### ${name}`).join(', ')}.`,
  );
}

if (bulletCount(sections) === 0) {
  fail(
    'the changelog block contains no bullets. Add at least one user-visible change, ' +
      'or apply the skip-changelog label.',
  );
}

console.log(`changelog block ok (${bulletCount(sections)} bullet(s))`);
