// Wraps @semantic-release/commit-analyzer to cap automated bumps at 'minor'.
// Major version bumps must come from a deliberate operator action: either a
// manual `git tag vX.0.0` push or dispatching the release-bot workflow
// with forceReleaseType=major. They are NEVER inferred from commit footers
// (BREAKING CHANGE) or `!`-style headers (feat!: …).
//
// FORCE_RELEASE_TYPE (env): when set to major|minor|patch, that verdict is
// returned directly and the upstream commit-analyzer is skipped. Anything
// else is logged and ignored.
import * as commitAnalyzer from '@semantic-release/commit-analyzer';

const VALID_TYPES = new Set(['major', 'minor', 'patch']);

export async function analyzeCommits(pluginConfig, context) {
  const forced = process.env.FORCE_RELEASE_TYPE;
  if (forced) {
    if (VALID_TYPES.has(forced)) {
      context.logger.log(
        `commit-analyzer-no-major: FORCE_RELEASE_TYPE=${forced}; overriding commit-analyzer verdict.`,
      );
      return forced;
    }
    context.logger.log(
      `commit-analyzer-no-major: ignoring invalid FORCE_RELEASE_TYPE=${forced} (expected major|minor|patch).`,
    );
  }
  const result = await commitAnalyzer.analyzeCommits(pluginConfig, context);
  if (result === 'major') {
    context.logger.log(
      'commit-analyzer-no-major: upstream returned major; demoting to minor. ' +
        'Major bumps require FORCE_RELEASE_TYPE=major or a manual `git tag vX.0.0` push.',
    );
    return 'minor';
  }
  return result;
}
