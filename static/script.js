// JavaScript functionality for Hadits Online

// Theme management
let currentTheme = localStorage.getItem('theme') || 'light';

function toggleTheme() {
    currentTheme = currentTheme === 'light' ? 'dark' : 'light';
    localStorage.setItem('theme', currentTheme);
    applyTheme();
}

function applyTheme() {
    if (currentTheme === 'dark') {
        document.documentElement.classList.add('dark');
    } else {
        document.documentElement.classList.remove('dark');
    }
}

// Search functionality (search boxes are real <form>s now; these remain as
// fallbacks for any older inline handlers).
function performSearch() {
    const input = document.getElementById('searchInput');
    const query = input ? input.value.trim() : '';
    if (query) {
        window.location.href = '/search?q=' + encodeURIComponent(query);
    }
}

function performSearchDekstop() {
    const input = document.getElementById('searchInputDkstp');
    const query = input ? input.value.trim() : '';
    if (query) {
        window.location.href = '/search?q=' + encodeURIComponent(query);
    }
}

// Text copying with feedback
function copyToClipboard(text, button) {
    navigator.clipboard.writeText(text).then(function () {
        const originalText = button.innerHTML;
        button.innerHTML = '✓ Tersalin!';
        button.classList.add('bg-green-600', 'hover:bg-green-700');
        button.classList.remove('bg-emerald-600', 'hover:bg-emerald-700');

        setTimeout(function () {
            button.innerHTML = originalText;
            button.classList.remove('bg-green-600', 'hover:bg-green-700');
            button.classList.add('bg-emerald-600', 'hover:bg-emerald-700');
        }, 2000);
    }).catch(function (err) {
        console.error('Failed to copy: ', err);
        alert('Gagal menyalin teks. Silakan coba lagi.');
    });
}

// Share functionality
async function shareHadith(title, text, url) {
    if (navigator.share) {
        try {
            await navigator.share({
                title: title,
                text: text,
                url: url
            });
        } catch (err) {
            if (err.name !== 'AbortError') {
                copyShareLink(url);
            }
        }
    } else {
        copyShareLink(url);
    }
}

function copyShareLink(url) {
    navigator.clipboard.writeText(url).then(function () {
        alert('Link hadith berhasil disalin!');
    }).catch(function (err) {
        prompt('Salin link ini:', url);
    });
}

function isVisible(el) {
    return !!el && el.offsetParent !== null;
}

// Keyboard shortcut: "/" focuses the visible search box.
document.addEventListener('keydown', function (e) {
    if (e.key !== '/' || e.ctrlKey || e.metaKey || e.altKey) return;
    const tag = document.activeElement ? document.activeElement.tagName : '';
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || (document.activeElement && document.activeElement.isContentEditable)) return;
    const candidates = ['heroSearchInput', 'searchInputDkstp', 'searchInput', 'f-q'].map(function (id) { return document.getElementById(id); });
    const target = candidates.find(isVisible);
    if (target) {
        e.preventDefault();
        target.focus();
    }
});

// Loading state management
function showLoading(element) {
    const originalContent = element.innerHTML;
    element.innerHTML = '<div class="loading-spinner mx-auto"></div>';
    element.dataset.originalContent = originalContent;
}

function hideLoading(element) {
    if (element.dataset.originalContent) {
        element.innerHTML = element.dataset.originalContent;
        delete element.dataset.originalContent;
    }
}

// Search highlighting is done on the server now (<mark> in snippets).
function highlightSearchResults(searchTerm) {}

// Auto-save search preferences
function saveSearchPreference(key, value) {
    try { localStorage.setItem('search_' + key, value); } catch (e) {}
}

function getSearchPreference(key, defaultValue = '') {
    try { return localStorage.getItem('search_' + key) || defaultValue; } catch (e) { return defaultValue; }
}

// Submit feedback for search forms: disable the button and show "Mencari…".
function setSearchBusy(form, busy) {
    const btn = form.querySelector('button[type="submit"]');
    if (!btn) return;
    if (busy) {
        if (!btn.dataset.label) btn.dataset.label = btn.innerHTML;
        btn.disabled = true;
        btn.setAttribute('aria-busy', 'true');
        const compact = btn.offsetWidth < 80;
        btn.innerHTML = '<span class="inline-block w-4 h-4 border-2 border-current border-t-transparent rounded-full animate-spin" aria-hidden="true"></span>' +
            (compact ? '<span class="sr-only">Mencari…</span>' : '<span class="ml-2">Mencari…</span>');
    } else if (btn.dataset.label) {
        btn.innerHTML = btn.dataset.label;
        btn.disabled = false;
        btn.removeAttribute('aria-busy');
    }
}

function initSearchForms() {
    document.querySelectorAll('form[role="search"]').forEach(function (form) {
        form.addEventListener('submit', function (e) {
            const q = form.querySelector('[name="q"]');
            if (q && !q.value.trim()) {
                e.preventDefault();
                q.focus();
                return;
            }
            setSearchBusy(form, true);
        });
    });
}

// Back/forward cache restores the page with the busy button: reset it.
window.addEventListener('pageshow', function () {
    document.querySelectorAll('form[role="search"]').forEach(function (form) { setSearchBusy(form, false); });
});

// Mobile menu + active navigation link
function initNavbar() {
    const btn = document.getElementById('mobileMenuBtn');
    const menu = document.getElementById('mobileMenu');
    if (btn && menu) {
        btn.addEventListener('click', function () {
            const open = menu.classList.toggle('hidden') === false;
            btn.setAttribute('aria-expanded', open ? 'true' : 'false');
        });
    }
    const currentPath = window.location.pathname;
    document.querySelectorAll('.nav-link, .nav-link-m').forEach(function (link) {
        if (link.getAttribute('data-path') === currentPath) {
            link.setAttribute('aria-current', 'page');
            if (link.classList.contains('nav-link')) {
                link.classList.add('bg-white/20', 'text-emerald-50');
            } else {
                link.classList.add('bg-white/20', 'border-l-4', 'border-emerald-300');
            }
        }
    });
    // On the search page, prefill the navbar boxes with the current query.
    if (currentPath === '/search') {
        const q = new URLSearchParams(window.location.search).get('q') || '';
        ['searchInput', 'searchInputDkstp'].forEach(function (id) {
            const el = document.getElementById(id);
            if (el && !el.value) el.value = q;
        });
    }
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', function () {
    // Register Service Worker for PWA
    if ('serviceWorker' in navigator) {
        window.addEventListener('load', () => {
            navigator.serviceWorker.register('/service-worker.js')
                .then(reg => console.log('Service Worker registered'))
                .catch(err => console.log('Service Worker registration failed: ', err));
        });
    }

    // Apply saved theme
    applyTheme();
    initNavbar();
    initSearchForms();

    ['searchInput', 'searchInputDkstp'].forEach(function (id) {
        const input = document.getElementById(id);
        if (!input) return;
        // Auto-save search query
        input.addEventListener('input', function () {
            saveSearchPreference('lastQuery', this.value);
        });
        // Restore last search query as a hint
        const lastQuery = getSearchPreference('lastQuery');
        if (lastQuery && !input.value) {
            input.placeholder = 'Cari: "' + lastQuery + '"';
        }
    });

    // Smooth scroll for in-page anchors (instant when reduced motion is
    // requested); the target receives focus so keyboard users land there too.
    const reduceMotion = window.matchMedia && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    document.querySelectorAll('a[href^="#"]').forEach(anchor => {
        anchor.addEventListener('click', function (e) {
            const href = this.getAttribute('href');
            if (href.length < 2) return;
            const target = document.querySelector(href);
            if (!target) return;
            e.preventDefault();
            target.scrollIntoView({ behavior: reduceMotion ? 'auto' : 'smooth', block: 'start' });
            if (!target.hasAttribute('tabindex')) target.setAttribute('tabindex', '-1');
            target.focus({ preventScroll: true });
        });
    });
});

// Utility functions
function debounce(func, wait) {
    let timeout;
    return function executedFunction(...args) {
        const later = () => {
            clearTimeout(timeout);
            func(...args);
        };
        clearTimeout(timeout);
        timeout = setTimeout(later, wait);
    };
}

function throttle(func, limit) {
    let inThrottle;
    return function () {
        const args = arguments;
        const context = this;
        if (!inThrottle) {
            func.apply(context, args);
            inThrottle = true;
            setTimeout(() => inThrottle = false, limit);
        }
    }
}
