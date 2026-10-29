import assert from 'node:assert/strict';
import { eventually, withFixture } from './fixture.mjs';

await withFixture(async ({ page, api, base, workspace, path }) => {
  const id = path.split('/').pop();
  const rows = async () => (await api('GET', `${path}/rows`)).rows;
  await page.locator('.db-table tbody tr').first().waitFor();
  const before = (await rows()).map(row => row.id);
  await page.getByRole('button', { name: 'New', exact: true }).click();
  await eventually(async () => (await rows()).length === before.length + 1);
  assert.equal((await rows()).find(row => !before.includes(row.id)).props.status, 'todo');
  assert.ok(page.url().endsWith(`/p/${id}`), 'New keeps the collection open');
  await eventually(async () => await page.locator('.db-table tbody tr').count() === 2);

  const doc = await api('POST', '/api/pages', { title: 'Filtered embed', workspaceId: workspace.id });
  await api('PATCH', `/api/pages/${doc.id}`, {
    content: [{ id: 'db', type: 'database', props: { collectionId: id, local: JSON.stringify({ viewId: 'table', views: { table: { filters: [{ property: 'status', op: 'is', value: 'done' }] } } }) }, content: [], children: [] }],
  });
  await page.goto(`${base}/p/${doc.id}`);
  const embed = page.locator('.bn-db-embed');
  await embed.locator('.db-table tbody tr').first().waitFor();
  const previous = (await rows()).map(row => row.id);
  await embed.getByRole('button', { name: 'New', exact: true }).click();
  await eventually(async () => (await rows()).length === previous.length + 1);
  assert.equal((await rows()).find(row => !previous.includes(row.id)).props.status, 'done');
  // Ambiguous alternatives stay empty and produce one explanation.
  const config = await api('GET', path);
  config.views[0].filters = [{ property: 'status', op: 'is', value: '', values: ['todo', 'done'] }];
  await api('PUT', path, { views: config.views });
  await page.goto(`${base}/p/${id}`);
  await page.locator('.db-table tbody tr').first().waitFor();
  const old = (await rows()).map(row => row.id);
  await page.getByRole('button', { name: 'New', exact: true }).click();
  await eventually(async () => (await rows()).length === old.length + 1);
  assert.equal((await rows()).find(row => !old.includes(row.id)).props.status, undefined);
  await page.locator('.toast').filter({ hasText: 'This item does not match the filter, so it is not shown here.' }).waitFor();
  // An explicit empty board column must override a conflicting filter.
  config.views[1].filters = [{ property: 'status', op: 'is', value: 'todo' }];
  await api('PUT', path, { views: config.views });
  await page.reload();
  await page.getByRole('button', { name: 'Board', exact: true }).click();
  const emptyColumn = page.locator('.board-col').filter({ hasText: 'No Status' });
  await emptyColumn.locator('.board-add').waitFor();
  const previousBoard = (await rows()).map(row => row.id);
  await emptyColumn.locator('.board-add').click();
  await eventually(async () => (await rows()).length === previousBoard.length + 1);
  assert.equal((await rows()).find(row => !previousBoard.includes(row.id)).props.status, '');
  console.log('Filtered creation: persisted values, visible row, unchanged navigation and embed passed.');
});
