// Small progressive enhancements. Every page works without any of this.
(function () {
	'use strict';

	// --- Theme toggle -------------------------------------------------------
	var root = document.documentElement;
	var stored = null;
	try { stored = localStorage.getItem('rb-theme'); } catch (e) { /* private mode */ }
	if (stored === 'light' || stored === 'dark') {
		root.setAttribute('data-theme', stored);
	}
	var toggle = document.querySelector('[data-theme-toggle]');
	if (toggle) {
		toggle.addEventListener('click', function () {
			var dark = root.getAttribute('data-theme') === 'dark' ||
				(root.getAttribute('data-theme') !== 'light' &&
					window.matchMedia('(prefers-color-scheme: dark)').matches);
			var next = dark ? 'light' : 'dark';
			root.setAttribute('data-theme', next);
			try { localStorage.setItem('rb-theme', next); } catch (e) { /* ignore */ }
		});
	}

	// --- Confirm before destructive submits ---------------------------------
	// The attribute sits on the button rather than the form when one form has
	// several submits, so look in both places.
	document.querySelectorAll('form[data-confirm]').forEach(function (form) {
		form.addEventListener('submit', function (event) {
			if (event.submitter && event.submitter.hasAttribute('data-confirm')) { return; }
			if (!window.confirm(form.getAttribute('data-confirm'))) { event.preventDefault(); }
		});
	});
	document.querySelectorAll('button[data-confirm]').forEach(function (button) {
		button.addEventListener('click', function (event) {
			if (!window.confirm(button.getAttribute('data-confirm'))) { event.preventDefault(); }
		});
	});

	// --- Live headcount on the registration forms ---------------------------
	// Saying the total back catches the commonest slip — leaving both boxes at
	// zero and expecting to be counted.
	document.querySelectorAll('[data-count-group]').forEach(function (group) {
		var hint = group.querySelector('[data-count-hint]');
		if (!hint) { return; }
		var update = function () {
			var people = 0;
			group.querySelectorAll('[data-count="people"]').forEach(function (input) {
				var n = parseInt(input.value, 10);
				if (!isNaN(n) && n > 0) { people += n; }
			});
			if (people === 0) {
				hint.textContent = group.getAttribute('data-count-zero') || 'Ingen anmäld.';
			} else {
				hint.textContent = people + (people === 1 ? ' person' : ' personer') + '.';
			}
		};
		group.querySelectorAll('[data-count="people"]').forEach(function (input) {
			input.addEventListener('input', update);
		});
		update();
	});

	// --- Drag the cooking teams into order ----------------------------------
	// The arrows underneath do the same job one step at a time, so this is an
	// improvement rather than the only way in.
	var orderList = document.querySelector('[data-order-list]');
	var orderForm = document.querySelector('[data-order-form]');
	if (orderList && orderForm && 'draggable' in document.createElement('li')) {
		var dragged = null;

		var renumber = function () {
			var ids = [];
			orderList.querySelectorAll('.order-item').forEach(function (item, i) {
				ids.push(item.getAttribute('data-id'));
				var rank = item.querySelector('.order-rank');
				if (rank) { rank.textContent = i + 1; }
			});
			orderForm.querySelector('[data-order-value]').value = ids.join(',');
		};

		orderList.addEventListener('dragstart', function (event) {
			dragged = event.target.closest('.order-item');
			if (!dragged) { return; }
			dragged.classList.add('is-dragging');
			event.dataTransfer.effectAllowed = 'move';
			// Firefox refuses to start a drag without something on the clipboard.
			event.dataTransfer.setData('text/plain', dragged.getAttribute('data-id'));
		});

		orderList.addEventListener('dragover', function (event) {
			if (!dragged) { return; }
			event.preventDefault();
			var over = event.target.closest('.order-item');
			if (!over || over === dragged) { return; }
			var box = over.getBoundingClientRect();
			var below = event.clientY > box.top + box.height / 2;
			orderList.insertBefore(dragged, below ? over.nextSibling : over);
		});

		orderList.addEventListener('dragend', function () {
			if (!dragged) { return; }
			dragged.classList.remove('is-dragging');
			dragged = null;
			renumber();
			// Saving straight away keeps the page and the database from
			// disagreeing about what the order is.
			orderForm.requestSubmit
				? orderForm.requestSubmit(orderForm.querySelector('[data-order-save]'))
				: orderForm.submit();
		});
	}

	// --- Who is in the house ------------------------------------------------
	// A household says who it is by its Mattermost account, and a cooking team
	// names its leader the same way. Remembering usernames is not something
	// anybody should have to do, so the field is a plain text input that this
	// turns into a combobox: the whole house is fetched once, indexed by
	// members.js, and searched as you type — by full name or by username,
	// whichever you happen to know. Without JavaScript, or without a chat
	// server, typing the username by hand does exactly the same thing.
	document.querySelectorAll('input[data-member-search]').forEach(function (field) {
		var list = document.getElementById(field.getAttribute('aria-controls'));
		if (!list || !window.fetch || !window.RBMembers) { return; }

		var nameSelector = field.getAttribute('data-member-name');
		var nameField = nameSelector ? document.querySelector(nameSelector) : null;
		var index = null;      // the searchable directory, once fetched
		var remote = false;    // ask the server to search instead of indexing
		var broken = false;    // the last lookup did not reach Mattermost
		var loading = null;    // the fetch in flight, so it happens once
		var shown = [];        // what the list currently offers
		var active = -1;       // which option the keyboard is on
		var wait = null;
		var inflight = null;

		var close = function () {
			list.hidden = true;
			list.innerHTML = '';
			field.setAttribute('aria-expanded', 'false');
			field.removeAttribute('aria-activedescendant');
			shown = [];
			active = -1;
		};

		var highlight = function (next) {
			var options = list.children;
			if (!options.length) { return; }
			if (active >= 0 && options[active]) {
				options[active].removeAttribute('aria-selected');
			}
			active = (next + options.length) % options.length;
			options[active].setAttribute('aria-selected', 'true');
			field.setAttribute('aria-activedescendant', options[active].id);
			if (options[active].scrollIntoView) {
				options[active].scrollIntoView({ block: 'nearest' });
			}
		};

		var choose = function (user) {
			if (!user) { return; }
			// The form submits the username, so that is what the field holds.
			field.value = user.username;
			if (nameField && !nameField.value.trim()) {
				nameField.value = user.name || '';
			}
			close();
		};

		// note puts a sentence where the people would go. Offering nothing at
		// all is indistinguishable from a field that does not work, which is
		// exactly how an unreachable chat server used to look.
		var note = function (message) {
			if (!message) { close(); return; }
			shown = [];
			active = -1;
			list.innerHTML = '';
			var item = document.createElement('li');
			item.className = 'combo-note';
			item.textContent = message;
			list.appendChild(item);
			list.hidden = false;
			field.setAttribute('aria-expanded', 'true');
		};

		var render = function (users) {
			shown = users;
			list.innerHTML = '';
			if (!users.length) {
				note(field.getAttribute(broken ? 'data-member-error' : 'data-member-none'));
				return;
			}
			users.forEach(function (user, i) {
				var option = document.createElement('li');
				option.id = list.id + '-' + i;
				option.className = 'combo-option';
				option.setAttribute('role', 'option');
				var name = document.createElement('strong');
				name.textContent = user.name || user.username;
				var handle = document.createElement('span');
				handle.textContent = '@' + user.username;
				option.appendChild(name);
				option.appendChild(handle);
				// mousedown, not click: the field blurs before a click lands.
				option.addEventListener('mousedown', function (event) {
					event.preventDefault();
					choose(user);
				});
				list.appendChild(option);
			});
			list.hidden = false;
			field.setAttribute('aria-expanded', 'true');
			active = -1;
		};

		// ask lets the server search, for a house too large to send at once.
		var ask = function (term) {
			if (inflight) { inflight.abort(); }
			inflight = window.AbortController ? new AbortController() : null;
			fetch('/medlemmar?q=' + encodeURIComponent(term),
				{ signal: inflight ? inflight.signal : undefined,
				  credentials: 'same-origin',
				  headers: { 'Accept': 'application/json' } })
				.then(function (response) {
					if (!response.ok) { throw new Error('status ' + response.status); }
					return response.json();
				})
				.then(function (data) {
					broken = !!data.unreachable;
					render(data.users || []);
				})
				.catch(function (err) {
					// An aborted request is the next keystroke's, not a failure.
					if (err && err.name === 'AbortError') { return; }
					broken = true;
					note(field.getAttribute('data-member-error'));
				});
		};

		// load fetches the directory once, and remembers if it was too big.
		var load = function () {
			if (loading) { return loading; }
			loading = fetch('/medlemmar',
				{ credentials: 'same-origin', headers: { 'Accept': 'application/json' } })
				.then(function (response) {
					if (!response.ok) { throw new Error('status ' + response.status); }
					return response.json();
				})
				.then(function (data) {
					// askServer covers both a house too large to send and one
					// the bot may not list — listing everybody needs rights a
					// bot token usually lacks, while searching does not. Either
					// way the picker keeps working by asking us instead.
					remote = !!data.askServer;
					broken = !!data.unreachable;
					index = remote ? null : window.RBMembers.buildIndex(data.users || []);
				})
				.catch(function () {
					// Even the request failed. Searching on the server is the
					// only route left, so let typing try it.
					index = null;
					remote = true;
					broken = true;
				});
			return loading;
		};

		var update = function () {
			var term = field.value.trim();
			if (term.length < 1) { close(); return; }
			if (remote) {
				if (term.length >= 2) { ask(term); }
				return;
			}
			load().then(function () {
				if (field.value.trim() !== term) { return; }
				if (!index) {
					// load() decided we cannot hold the directory ourselves.
					if (term.length >= 2) { ask(term); } else { close(); }
					return;
				}
				render(window.RBMembers.search(index, term));
			});
		};

		field.setAttribute('role', 'combobox');
		field.setAttribute('aria-expanded', 'false');
		field.setAttribute('aria-autocomplete', 'list');
		field.addEventListener('focus', load);
		field.addEventListener('input', function () {
			clearTimeout(wait);
			// The list is local, so there is nothing to wait for. The debounce
			// is only there for the server-side fallback.
			wait = setTimeout(update, remote ? 250 : 0);
		});

		field.addEventListener('keydown', function (event) {
			if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
				if (list.hidden) { update(); return; }
				// The list may be holding a sentence rather than people, and a
				// sentence is not something to arrow onto.
				if (!shown.length) { return; }
				event.preventDefault();
				highlight(active + (event.key === 'ArrowDown' ? 1 : -1));
				return;
			}
			if (event.key === 'Enter' && !list.hidden && active >= 0) {
				event.preventDefault();
				choose(shown[active]);
				return;
			}
			if (event.key === 'Escape' && !list.hidden) {
				event.preventDefault();
				close();
			}
		});

		field.addEventListener('blur', close);
	});

	// --- Dates ---------------------------------------------------------------
	// A date is three selectors reading year-month-day; see datefield.go for
	// why it is not an <input type="date">. Both jobs here are polish on top of
	// something that already works: shortening the day list to the month that
	// is chosen, and saying the date back in words. Without this the day list
	// is simply always 31 long, and a 31st of February is refused on the way in
	// rather than being unpickable.
	document.querySelectorAll('[data-datefield]').forEach(function (field) {
		var year = field.querySelector('[data-date="year"]');
		var month = field.querySelector('[data-date="month"]');
		var day = field.querySelector('[data-date="day"]');
		var prose = field.querySelector('[data-date-prose]');
		if (!year || !month || !day) { return; }

		var months = (field.getAttribute('data-date-months') || '').split(',');

		// daysIn is the length of the chosen month. With no year chosen yet,
		// February is given 29 days: offering a day that turns out not to
		// exist is recoverable, hiding one that does is not.
		var daysIn = function () {
			var m = parseInt(month.value, 10);
			if (!m) { return 31; }
			var y = parseInt(year.value, 10);
			if (!y) { return m === 2 ? 29 : new Date(2024, m, 0).getDate(); }
			// Day zero of the next month is the last day of this one.
			return new Date(y, m, 0).getDate();
		};

		var trim = function () {
			var limit = daysIn();
			var chosen = parseInt(day.value, 10);
			day.querySelectorAll('option').forEach(function (option) {
				var n = parseInt(option.value, 10);
				if (!n) { return; }
				// Hidden as well as disabled: a disabled option is still
				// listed, and a 30th of February should not be on the list at
				// all.
				option.hidden = n > limit;
				option.disabled = n > limit;
			});
			// A day that has just stopped existing — the 31st, and then April
			// — moves to the last day of the month rather than being silently
			// dropped, which would submit a date nobody chose.
			if (chosen > limit) { day.value = String(limit).padStart(2, '0'); }
		};

		var say = function () {
			if (!prose) { return; }
			var y = parseInt(year.value, 10);
			var m = parseInt(month.value, 10);
			var d = parseInt(day.value, 10);
			if (!y || !m || !d || !months[m - 1]) { prose.textContent = ''; return; }
			prose.textContent = d + ' ' + months[m - 1] + ' ' + y;
		};

		[year, month, day].forEach(function (part) {
			part.addEventListener('change', function () {
				trim();
				say();
			});
		});
		trim();
	});

	// --- Print button on the list -------------------------------------------
	var print = document.querySelector('[data-print]');
	if (print) {
		print.addEventListener('click', function () { window.print(); });
	}
})();
