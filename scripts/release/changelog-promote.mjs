import { readFile, writeFile } from 'node:fs/promises';
import {
  ALLOWED_SECTIONS,
  extractBlock,
  parseBlock,
  fetchPullRequestsForCommit,
} from './changelog-block.mjs';

// The CHANGELOG uses unbracketed headers:
//   ## 0.5.0 (2026-07-03)
// The release workflow (.github/workflows/release.yml) extracts the section
// for a tag by matching "## ${VERSION}" at the start of a line, so we MUST
// keep producing that same shape. Do NOT switch to "## [X.Y.Z] - date".
const CHANGELOG_PATH = 'CHANGELOG.md';
const VERSION_PATH = 'version.go';
// The bot inserts each new release section immediately below this anchor.
// The regex matches a line that IS the anchor (with optional trailing
// whitespace) — never an inline-code occurrence in surrounding prose.
const ANCHOR_PATTERN = /^<!-- changelog:insert-here -->\s*$/m;

export async function prepare(_pluginConfig, context) {
  const { nextRelease, logger, commits, env } = context;
  const date = new Date().toISOString().slice(0, 10);
  const newHeader = `## ${nextRelease.version} (${date})`;

  let content = await readFile(CHANGELOG_PATH, 'utf8');
  const anchorMatch = content.match(ANCHOR_PATTERN);
  if (!anchorMatch) {
    throw new Error(
      `${CHANGELOG_PATH} is missing the changelog insertion anchor (a line containing exactly "<!-- changelog:insert-here -->")`,
    );
  }

  const repository = env.GITHUB_REPOSITORY;
  if (!repository) {
    throw new Error('GITHUB_REPOSITORY required to fetch PR changelog blocks');
  }

  const seenPrs = new Set();
  const aggregate = Object.fromEntries(ALLOWED_SECTIONS.map((name) => [name, []]));
  const skippedPrs = [];
  const blocklessPrs = [];

  for (const commit of commits || []) {
    const sha = commit.commit?.long || commit.hash;
    if (!sha) continue;
    let pulls;
    try {
      pulls = await fetchPullRequestsForCommit(repository, sha, env);
    } catch (error) {
      logger.log(`changelog: could not fetch PRs for ${sha.slice(0, 8)}: ${error.message}`);
      continue;
    }
    for (const pull of pulls) {
      if (seenPrs.has(pull.number)) continue;
      seenPrs.add(pull.number);

      const labels = (pull.labels || []).map((label) => label.name);
      if (labels.includes('skip-changelog')) {
        skippedPrs.push(`#${pull.number}`);
        continue;
      }

      const block = extractBlock(pull.body || '');
      if (block === null) {
        blocklessPrs.push(`#${pull.number}`);
        continue;
      }

      const { sections } = parseBlock(block);
      for (const section of ALLOWED_SECTIONS) {
        const items = sections[section];
        if (!items || items.length === 0) continue;
        for (const item of items) {
          aggregate[section].push(`${item} (#${pull.number})`);
        }
      }
    }
  }

  const versionBody = renderVersionBody(aggregate, { skippedPrs, blocklessPrs });

  // Auto-generated commit-summary fallback. Useful in code review and for
  // releases that aggregate zero PR-supplied bullets.
  const bullets = (nextRelease.notes ?? '')
    .replace(/^#{1,2}\s+\[?[\d.]+[^\n]*\n+/, '')
    .trim();
  const autoBlock = bullets
    ? `\n<details>\n<summary>Commit summary (auto-generated)</summary>\n\n${bullets}\n\n</details>\n`
    : '';

  const anchorEnd = anchorMatch.index + anchorMatch[0].length;
  const insertion = `\n\n${newHeader}\n${versionBody}${autoBlock}`;
  content = content.slice(0, anchorEnd) + insertion + content.slice(anchorEnd);

  await writeFile(CHANGELOG_PATH, content);

  // Keep the fallback version baked into the binary in sync with the tag.
  const versionContent = await readFile(VERSION_PATH, 'utf8');
  const updatedVersionContent = versionContent.replace(
    /var Version string = "[^"]*"/,
    `var Version string = "${nextRelease.version}"`,
  );
  if (updatedVersionContent === versionContent) {
    throw new Error(`${VERSION_PATH} does not contain the expected Version declaration`);
  }
  await writeFile(VERSION_PATH, updatedVersionContent);

  const totals = ALLOWED_SECTIONS.map(
    (section) => `${section}=${aggregate[section].length}`,
  ).join(', ');
  logger.log(`changelog: inserted ${newHeader} below anchor (${totals})`);
  if (skippedPrs.length > 0) {
    logger.log(`changelog: skip-changelog PRs: ${skippedPrs.join(', ')}`);
  }
  if (blocklessPrs.length > 0) {
    logger.log(
      `changelog: PRs without a changelog block (legacy or pre-template): ${blocklessPrs.join(', ')}`,
    );
  }
}

function renderVersionBody(aggregate, { skippedPrs, blocklessPrs }) {
  const parts = [];
  for (const section of ALLOWED_SECTIONS) {
    const items = aggregate[section];
    if (items.length === 0) continue;
    parts.push(`\n### ${section}\n\n${items.join('\n')}\n`);
  }
  if (parts.length === 0) {
    const note =
      skippedPrs.length + blocklessPrs.length > 0
        ? '\n_No changelog entries (all merged PRs opted out or shipped without a block)._\n'
        : '\n_No changelog entries._\n';
    return note;
  }
  return parts.join('');
}
