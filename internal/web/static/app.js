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

	// --- Live arithmetic on the count fields --------------------------------
	// Saying "3 of you eat everything" as the numbers are typed catches the
	// common mistake — more vegans than people — before the server has to.
	document.querySelectorAll('[data-count-group]').forEach(function (group) {
		var hint = group.querySelector('[data-count-hint]');
		if (!hint) { return; }
		var read = function (selector) {
			var total = 0;
			group.querySelectorAll(selector).forEach(function (input) {
				var n = parseInt(input.value, 10);
				if (!isNaN(n) && n > 0) { total += n; }
			});
			return total;
		};
		var update = function () {
			var people = read('[data-count="people"]');
			var diet = read('[data-count="diet"]');
			hint.classList.remove('is-bad');
			if (people === 0) {
				hint.textContent = group.getAttribute('data-count-zero') || 'Ingen anmäld.';
			} else if (diet > people) {
				hint.textContent = 'Det är fler veganer och vegetarianer än ni är personer.';
				hint.classList.add('is-bad');
			} else {
				var rest = people - diet;
				hint.textContent = people + (people === 1 ? ' person' : ' personer') +
					', varav ' + rest + (rest === 1 ? ' äter' : ' äter') + ' allt.';
			}
		};
		group.querySelectorAll('[data-count]').forEach(function (input) {
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

	// --- Print button on the list -------------------------------------------
	var print = document.querySelector('[data-print]');
	if (print) {
		print.addEventListener('click', function () { window.print(); });
	}
})();
