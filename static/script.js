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

// Search functionality
function performSearch() {
    const query = document.getElementById('searchInput').value.trim();
    if (query) {
        window.location.href = '/search?q=' + encodeURIComponent(query);
    }
}

// Text copying with feedback
function copyToClipboard(text, button) {
    navigator.clipboard.writeText(text).then(function() {
        const originalText = button.innerHTML;
        button.innerHTML = '✓ Tersalin!';
        button.classList.add('bg-green-600', 'hover:bg-green-700');
        button.classList.remove('bg-emerald-600', 'hover:bg-emerald-700');
        
        setTimeout(function() {
            button.innerHTML = originalText;
            button.classList.remove('bg-green-600', 'hover:bg-green-700');
            button.classList.add('bg-emerald-600', 'hover:bg-emerald-700');
        }, 2000);
    }).catch(function(err) {
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
    navigator.clipboard.writeText(url).then(function() {
        alert('Link hadith berhasil disalin!');
    }).catch(function(err) {
        prompt('Salin link ini:', url);
    });
}

// Keyboard navigation
document.addEventListener('keydown', function(e) {
    // Ctrl/Cmd + Arrow keys for hadith navigation
    if ((e.ctrlKey || e.metaKey)) {
        if (e.key === 'ArrowLeft') {
            const prevLink = document.querySelector('a[href*="/collection/"][href*="/"]');
            if (prevLink && prevLink.textContent.includes('Hadith')) {
                window.location.href = prevLink.href;
            }
        } else if (e.key === 'ArrowRight') {
            const nextLink = document.querySelector('a[href*="/collection/"][href*="/"]');
            if (nextLink && nextLink.textContent.includes('Hadith')) {
                const allLinks = document.querySelectorAll('a[href*="/collection/"][href*="/"]');
                if (allLinks.length > 1) {
                    // Find the "next" link (second one in this case)
                    const nextButton = Array.from(allLinks).find(link => 
                        link.textContent.includes('Hadith') && 
                        link.href.includes('>') || 
                        link.href.includes('Selanjutnya')
                    );
                    if (nextButton) {
                        window.location.href = nextButton.href;
                    }
                }
            }
        }
    }
    
    // Focus search with / key
    if (e.key === '/' && !e.ctrlKey && !e.metaKey) {
        const searchInput = document.getElementById('searchInput');
        if (searchInput && document.activeElement !== searchInput) {
            e.preventDefault();
            searchInput.focus();
        }
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

// Search highlighting
function highlightSearchResults(searchTerm) {
    if (!searchTerm) return;
    
    const elements = document.querySelectorAll('.searchable-content');
    const regex = new RegExp(`(${searchTerm})`, 'gi');
    
    elements.forEach(element => {
        const originalText = element.textContent;
        const highlightedText = originalText.replace(regex, '<mark class="search-highlight">$1</mark>');
        element.innerHTML = highlightedText;
    });
}

// Auto-save search preferences
function saveSearchPreference(key, value) {
    localStorage.setItem('search_' + key, value);
}

function getSearchPreference(key, defaultValue = '') {
    return localStorage.getItem('search_' + key) || defaultValue;
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', function() {
    // Apply saved theme
    applyTheme();
    
    // Add search enter key handler
    const searchInput = document.getElementById('searchInput');
    if (searchInput) {
        searchInput.addEventListener('keypress', function(e) {
            if (e.key === 'Enter') {
                performSearch();
            }
        });
        
        // Auto-save search query
        searchInput.addEventListener('input', function() {
            saveSearchPreference('lastQuery', this.value);
        });
        
        // Restore last search query
        const lastQuery = getSearchPreference('lastQuery');
        if (lastQuery && !searchInput.value) {
            searchInput.placeholder = `Cari: "${lastQuery}"`;
        }
    }
    
    // Add smooth scroll for anchor links
    document.querySelectorAll('a[href^="#"]').forEach(anchor => {
        anchor.addEventListener('click', function(e) {
            e.preventDefault();
            const target = document.querySelector(this.getAttribute('href'));
            if (target) {
                target.scrollIntoView({
                    behavior: 'smooth',
                    block: 'start'
                });
            }
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
    return function() {
        const args = arguments;
        const context = this;
        if (!inThrottle) {
            func.apply(context, args);
            inThrottle = true;
            setTimeout(() => inThrottle = false, limit);
        }
    }
}
