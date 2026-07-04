// Shared parser for the per-PR changelog block.
//
// The block is delimited in a PR description by:
//
//     <!-- changelog:start -->
//     ### Added
//     - one bullet per user-visible change
//     <!-- changelog:end -->
//
// Sections follow Keep a Changelog (https://keepachangelog.com/).

export const START_MARKER = '<!-- changelog:start -->';
export const END_MARKER = '<!-- changelog:end -->';

export const ALLOWED_SECTIONS = [
  'Added',
  'Changed',
  'Deprecated',
  'Removed',
  'Fixed',
  'Security',
];

// Bullets like "- TODO: replace with a one-line summary…" that the
// template ships with. The PR author must replace them; if any survive
// into the merge, CI rejects. (The unedited "### Added | Changed | …"
// pseudo-heading is independently caught by the unknown-section check.)
export const PLACEHOLDER_PATTERN = /^-\s+TODO:/;

// Extracts the raw block text between START_MARKER and END_MARKER, exclusive.
// Returns null when either marker is missing or they are out of order.
export function extractBlock(description) {
  if (!description) return null;
  const start = description.indexOf(START_MARKER);
  if (start === -1) return null;
  const end = description.indexOf(END_MARKER, start + START_MARKER.length);
  if (end === -1) return null;
  return description.slice(start + START_MARKER.length, end);
}

// Parses a block into { [sectionName]: ["- bullet", ...] }. Sections not in
// ALLOWED_SECTIONS are returned in `unknownSections`. Bullets that match
// PLACEHOLDER_PATTERN are returned in `placeholders` so the caller can
// fail or warn.
//
// HTML comments inside the block (including multi-line ones such as the
// template's own helper comment with `### Added — new behavior` example
// lines) are stripped before line-parsing so they don't get mistaken for
// real section headings or bullets.
export function parseBlock(block) {
  const stripped = block.replace(/<!--[\s\S]*?-->/g, '');

  const sections = {};
  const placeholders = [];
  const unknownSections = [];

  let currentSection = null;
  for (const rawLine of stripped.split('\n')) {
    const line = rawLine.replace(/\s+$/, '');
    const trimmed = line.trim();

    const heading = trimmed.match(/^###\s+(.+?)\s*$/);
    if (heading) {
      const name = heading[1];
      if (ALLOWED_SECTIONS.includes(name)) {
        currentSection = name;
        if (!sections[currentSection]) sections[currentSection] = [];
      } else {
        currentSection = null;
        unknownSections.push(name);
      }
      continue;
    }

    if (!currentSection) continue;
    if (!trimmed) continue;

    if (PLACEHOLDER_PATTERN.test(trimmed)) {
      placeholders.push(trimmed);
      continue;
    }

    sections[currentSection].push(line);
  }

  return { sections, placeholders, unknownSections };
}

// Returns the total bullet count across all known sections.
export function bulletCount(sections) {
  return Object.values(sections).reduce((sum, items) => sum + items.length, 0);
}

// Fetches PRs associated with a commit SHA via the GitHub REST API. Returns
// [] when the commit has no PR (e.g. the chore(release) commit itself).
export async function fetchPullRequestsForCommit(repository, sha, env) {
  const apiUrl = env.GITHUB_API_URL || 'https://api.github.com';
  const url = `${apiUrl}/repos/${repository}/commits/${sha}/pulls`;
  const response = await fetch(url, { headers: authHeaders(env) });
  if (!response.ok) {
    throw new Error(
      `GitHub API ${response.status} for ${url}: ${await response.text()}`,
    );
  }
  return response.json();
}

function authHeaders(env) {
  const token = env.GH_TOKEN || env.GITHUB_TOKEN;
  if (!token) {
    throw new Error('no GitHub token available (set GH_TOKEN or GITHUB_TOKEN)');
  }
  return {
    Authorization: `Bearer ${token}`,
    Accept: 'application/vnd.github+json',
  };
}
