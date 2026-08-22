// Finding somebody in the house by name or username, in the browser.
//
// The page is handed everyone in the house, so searching is typing rather than
// a request per keystroke. Every word of every name and username goes into a
// trie whose nodes carry the people they belong to, so a prefix walks straight
// to its matches without looking at anyone else.
//
// "Anna Andersson" is indexed under "anna" and "andersson"; her account
// anna.andersson under the same two. Accents are folded, so "Östberg" and
// "ostberg" find each other whichever way it is typed.
//
// The file exposes window.RBMembers and touches no DOM, which is what lets
// `node --test` check the searching on its own.
(function (root) {
	'use strict';

	// The accented letters that turn up in the house's names. The server folds
	// with the same table, so both ends agree on what matches.
	var ACCENTS = {
		'å': 'a', 'ä': 'a', 'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a',
		'ö': 'o', 'ø': 'o', 'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o',
		'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
		'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
		'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
		'ý': 'y', 'ÿ': 'y', 'ñ': 'n', 'ç': 'c', 'ð': 'd', 'þ': 'th', 'ß': 'ss',
		'æ': 'ae', 'œ': 'oe', 'ł': 'l', 'š': 's', 'ž': 'z', 'č': 'c', 'ř': 'r'
	};

	// fold lowercases and strips accents: the form both ends compare in.
	function fold(text) {
		var lower = String(text == null ? '' : text).toLowerCase();
		var out = '';
		for (var i = 0; i < lower.length; i++) {
			var ch = lower.charAt(i);
			out += (ACCENTS[ch] || ch);
		}
		return out;
	}

	// words splits a name or username into the pieces worth indexing:
	// "Anna Andersson" and "anna.andersson" both give ["anna", "andersson"].
	function words(text) {
		var parts = fold(text).split(/[^a-z0-9]+/);
		var out = [];
		for (var i = 0; i < parts.length; i++) {
			if (parts[i]) { out.push(parts[i]); }
		}
		return out;
	}

	function newNode() {
		return { kids: Object.create(null), ids: [] };
	}

	// insert records id under every prefix of word. Holding the members at each
	// node rather than only at the end means a search is a walk down the word
	// and no traversal at all — the node it lands on is already the answer.
	//
	// All the words of one member are inserted together, so a name that repeats
	// a word ("Anna Anna") can only ever duplicate the id last pushed.
	function insert(node, word, id) {
		for (var i = 0; i < word.length; i++) {
			var ch = word.charAt(i);
			var next = node.kids[ch];
			if (!next) {
				next = newNode();
				node.kids[ch] = next;
			}
			node = next;
			if (node.ids[node.ids.length - 1] !== id) {
				node.ids.push(id);
			}
		}
	}

	// buildIndex prepares a list of {username, name} for searching.
	function buildIndex(users) {
		var list = (users || []).slice();
		var trie = newNode();
		for (var id = 0; id < list.length; id++) {
			var user = list[id] || {};
			var keys = words(user.name).concat(words(user.username));
			for (var k = 0; k < keys.length; k++) {
				insert(trie, keys[k], id);
			}
		}
		return { users: list, trie: trie };
	}

	// idsFor walks the trie to a prefix and returns the members under it.
	function idsFor(trie, prefix) {
		var node = trie;
		for (var i = 0; i < prefix.length; i++) {
			node = node.kids[prefix.charAt(i)];
			if (!node) { return []; }
		}
		return node.ids;
	}

	// rank puts the most likely person first: a match on the start of the whole
	// name or username beats a match on the start of a later word, which beats
	// anything else. "and" offers Anna Andersson before Bo Andersson-Ek only
	// because of the alphabet, which is as good a reason as any.
	function rank(user, query, first) {
		var name = fold(user.name);
		var username = fold(user.username);
		if (name.indexOf(query) === 0 || username.indexOf(query) === 0) { return 0; }
		var parts = words(user.name).concat(words(user.username));
		for (var i = 0; i < parts.length; i++) {
			if (parts[i].indexOf(first) === 0) { return 1; }
		}
		return 2;
	}

	// search returns the members matching every word of the query, best first.
	// Several words all have to match something ("mikael öst" finds Mikael
	// Östberg but not Mikael Ek), which is how a full name narrows a list.
	function search(index, query, limit) {
		if (!index || !index.trie) { return []; }
		var folded = fold(query).replace(/^@/, '').trim();
		var parts = words(folded);
		if (!parts.length) { return []; }

		var hits = null;
		for (var i = 0; i < parts.length; i++) {
			var ids = idsFor(index.trie, parts[i]);
			if (!ids.length) { return []; }
			if (hits === null) {
				hits = ids.slice();
				continue;
			}
			// Every word has to point at the same person.
			var keep = {};
			for (var j = 0; j < ids.length; j++) { keep[ids[j]] = true; }
			var narrowed = [];
			for (var k = 0; k < hits.length; k++) {
				if (keep[hits[k]]) { narrowed.push(hits[k]); }
			}
			hits = narrowed;
			if (!hits.length) { return []; }
		}

		var found = [];
		for (var h = 0; h < hits.length; h++) {
			found.push(index.users[hits[h]]);
		}
		found.sort(function (a, b) {
			var byRank = rank(a, folded, parts[0]) - rank(b, folded, parts[0]);
			if (byRank !== 0) { return byRank; }
			return String(a.name).localeCompare(String(b.name), 'sv');
		});
		return found.slice(0, limit || 8);
	}

	root.RBMembers = { fold: fold, words: words, buildIndex: buildIndex, search: search };
})(typeof window !== 'undefined' ? window : globalThis);
