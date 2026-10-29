import assert from 'node:assert/strict';
import { createHash, randomBytes } from 'node:crypto';
import { withFixture } from './fixture.mjs';

// The consent screen promised that a connection could be ended "in your account
// settings", and no such place existed (#115). It is API tokens → Signed-in
// connections now: the consent screen names it, the row says what was granted,
// and ending it there cuts the agent off at once.
await withFixture(async ({ page, context, base, workspace }) => {
  page.setDefaultTimeout(15000);
  const redirect = `${base}/oauth-test-callback`;
  const client = await (
    await context.request.post(`${base}/oauth/register`, {
      data: { client_name: 'Example Agent', redirect_uris: [redirect] },
    })
  ).json();
  const verifier = randomBytes(32).toString('base64url');
  const challenge = createHash('sha256').update(verifier).digest('base64url');

  // The consent screen names the place, in the interface's own words.
  await page.goto(
    `${base}/oauth/authorize?` +
      new URLSearchParams({
        response_type: 'code',
        client_id: client.client_id,
        redirect_uri: redirect,
        code_challenge: challenge,
        code_challenge_method: 'S256',
      }),
  );
  await page.getByText('API tokens → Signed-in connections', { exact: false }).waitFor();

  // A connection made the way a client makes one, with the approval the
  // consent screen would send from this signed-in browser.
  const { code } = await (
    await context.request.post(`${base}/api/oauth/approve`, {
      data: {
        clientId: client.client_id,
        redirectUri: redirect,
        codeChallenge: challenge,
        codeChallengeMethod: 'S256',
        scope: 'write',
        resource: '',
        allWorkspaces: false,
        workspaces: [workspace.id],
      },
    })
  ).json();
  const token = await (
    await context.request.post(`${base}/oauth/token`, {
      form: {
        grant_type: 'authorization_code',
        code,
        client_id: client.client_id,
        redirect_uri: redirect,
        code_verifier: verifier,
      },
    })
  ).json();
  assert(token.access_token && token.refresh_token, JSON.stringify(token));
  // Plain fetch, not the browser's request context: the agent carries its
  // token and nothing else, no session cookie to fall back on.
  const asAgent = () =>
    fetch(`${base}/api/pages`, { headers: { Authorization: `Bearer ${token.access_token}` } });
  assert.equal((await asAgent()).status, 200, 'the new connection works');

  await page.goto(base + '/');
  await page.locator('.user-menu-btn').click();
  await page.getByRole('button', { name: 'API tokens', exact: true }).click();
  await page.getByRole('heading', { name: 'Signed-in connections', exact: true }).waitFor();
  const row = page.locator('.user-row', { hasText: 'Example Agent' });
  await row.waitFor();
  const shown = await row.innerText();
  for (const part of ['read-write', workspace.name, 'connected since', 'used ', '127.0.0.1']) {
    assert(shown.includes(part), `the row does not say "${part}": ${shown}`);
  }

  const deleted = page.waitForRequest(
    (r) => r.method() === 'DELETE' && r.url().includes('/api/oauth/grants/'),
  );
  await row.getByRole('button', { name: 'End connection', exact: true }).click();
  const ask = page.locator('.confirm-dialog');
  await ask.getByText('Example Agent', { exact: false }).waitFor();
  await ask.getByRole('button', { name: 'End connection', exact: true }).click();
  const answer = await (await deleted).response();
  assert.equal(answer?.status(), 200, 'the DELETE went through');

  await row.waitFor({ state: 'detached' });
  await page.getByText('No agent is signed in to your account.').waitFor();
  assert.deepEqual(await (await context.request.get(`${base}/api/oauth/grants`)).json(), []);
  assert.equal((await asAgent()).status, 401, 'the access token outlived the connection');
  const renewed = await context.request.post(`${base}/oauth/token`, {
    form: { grant_type: 'refresh_token', refresh_token: token.refresh_token, client_id: client.client_id },
  });
  assert.notEqual(renewed.status(), 200, 'the refresh token outlived the connection');

  console.log('Signed-in connections: listed, ended from the dialog, and cut off at once.');
});
