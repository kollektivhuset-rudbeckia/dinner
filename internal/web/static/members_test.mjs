// Tests for the member search. Run them with:
//
//	node --test internal/web/static/
//
// or `make test-js`, which skips them when Node is not installed. members.js
// is a plain browser script with no imports, so the test loads it with a stand
// in for `window` and reads what it exported.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const browser = {};
new Function('window', readFileSync(join(here, 'members.js'), 'utf8'))(browser);
const { fold, words, buildIndex, search } = browser.RBMembers;

const house = buildIndex([
	{ username: 'anna.andersson', name: 'Anna Andersson' },
	{ username: 'bo.bengtsson', name: 'Bo Bengtsson' },
	{ username: 'mikael.ostberg', name: 'Mikael Östberg' },
	{ username: 'mikael.ek', name: 'Mikael Ek' },
	{ username: 'farid', name: '' },
]);

const names = (query, limit) => search(house, query, limit).map((u) => u.username);

test('folding strips accents and capitals', () => {
	assert.equal(fold('Östberg'), 'ostberg');
	assert.equal(fold('Åsa Ängström'), 'asa angstrom');
	assert.equal(fold(null), '');
});

test('a name splits into the words worth indexing', () => {
	assert.deepEqual(words('Anna Andersson'), ['anna', 'andersson']);
	assert.deepEqual(words('anna.andersson'), ['anna', 'andersson']);
	assert.deepEqual(words('  '), []);
});

test('a full name finds the member', () => {
	assert.deepEqual(names('Anna Andersson'), ['anna.andersson']);
	assert.deepEqual(names('mikael östberg'), ['mikael.ostberg']);
});

test('a surname finds the member, accented or not', () => {
	assert.deepEqual(names('Östberg'), ['mikael.ostberg']);
	assert.deepEqual(names('ostberg'), ['mikael.ostberg']);
});

test('a username finds the member, with or without the @', () => {
	assert.deepEqual(names('bo.bengtsson'), ['bo.bengtsson']);
	assert.deepEqual(names('@bo.beng'), ['bo.bengtsson']);
});

test('a prefix offers everyone it could be', () => {
	assert.deepEqual(names('mikael'), ['mikael.ek', 'mikael.ostberg']);
	assert.deepEqual(names('b'), ['bo.bengtsson']);
});

test('every word has to match the same person', () => {
	assert.deepEqual(names('mikael öst'), ['mikael.ostberg']);
	assert.deepEqual(names('mikael andersson'), []);
});

test('the best match comes first', () => {
	// "an" starts Anna's name; it only appears inside Bengtsson's, so she wins
	// even though the alphabet would put neither first.
	assert.deepEqual(search(house, 'anna', 1)[0].username, 'anna.andersson');
	// A username is enough to be found when the account has no name at all.
	assert.deepEqual(names('farid'), ['farid']);
});

test('nothing matching means nothing offered', () => {
	assert.deepEqual(names('zzz'), []);
	assert.deepEqual(names(''), []);
	assert.deepEqual(names('   '), []);
	assert.deepEqual(names('@'), []);
});

test('the list is capped', () => {
	assert.equal(search(house, 'mikael', 1).length, 1);
	// Eight by default, which is as many as the picker shows.
	assert.ok(search(house, 'a').length <= 8);
});

test('an empty index answers instead of throwing', () => {
	assert.deepEqual(search(buildIndex([]), 'anna'), []);
	assert.deepEqual(search(null, 'anna'), []);
});
