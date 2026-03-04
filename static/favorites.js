// Favorites management functionality
class FavoritesManager {
    constructor() {
        this.favorites = this.loadFavorites();
        this.initEventListeners();
    }

    loadFavorites() {
        const saved = localStorage.getItem('hadith_favorites');
        return saved ? JSON.parse(saved) : [];
    }

    saveFavorites() {
        localStorage.setItem('hadith_favorites', JSON.stringify(this.favorites));
        this.updateFavoritesUI();
    }

    isFavorite(hadithKey) {
        return this.favorites.includes(hadithKey);
    }

    toggleFavorite(hadithKey, hadithData) {
        const index = this.favorites.indexOf(hadithKey);
        if (index > -1) {
            this.favorites.splice(index, 1);
            this.showNotification('Hadith dihapus dari favorit', 'info');
        } else {
            this.favorites.push(hadithKey);
            // Save hadith data to localStorage for offline viewing
            this.saveHadithData(hadithKey, hadithData);
            this.showNotification('Hadith ditambahkan ke favorit', 'success');
        }
        this.saveFavorites();
    }

    saveHadithData(key, data) {
        const allHadith = JSON.parse(localStorage.getItem('saved_hadith_data') || '{}');
        allHadith[key] = data;
        localStorage.setItem('saved_hadith_data', JSON.stringify(allHadith));
    }

    getHadithData(key) {
        const allHadith = JSON.parse(localStorage.getItem('saved_hadith_data') || '{}');
        return allHadith[key];
    }

    getFavorites() {
        return this.favorites.map(key => ({
            key: key,
            data: this.getHadithData(key)
        }));
    }

    updateFavoritesUI() {
        // Update all favorite buttons on the page
        document.querySelectorAll('.favorite-btn').forEach(btn => {
            const hadithKey = btn.dataset.hadithKey;
            this.updateFavoriteButton(btn, hadithKey);
        });

        // Update favorites count if exists
        const countElement = document.getElementById('favorites-count');
        if (countElement) {
            countElement.textContent = this.favorites.length;
        }
    }

    updateFavoriteButton(button, hadithKey) {
        const isFavorite = this.isFavorite(hadithKey);
        if (isFavorite) {
            button.classList.add('text-red-600', 'hover:text-red-700');
            button.classList.remove('text-gray-400', 'hover:text-gray-600');
            button.innerHTML = '<svg class="w-5 h-5" fill="currentColor" viewBox="0 0 20 20"><path d="M3.172 5.172a4 4 0 015.656 0L10 6.343l1.172-1.171a4 4 0 115.656 5.656L10 17.657l-6.828-6.829a4 4 0 010-5.656z"></path></svg>';
        } else {
            button.classList.remove('text-red-600', 'hover:text-red-700');
            button.classList.add('text-gray-400', 'hover:text-gray-600');
            button.innerHTML = '<svg class="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 20 20"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4.318 6.318a4.5 4.5 0 000 6.364L12 20.364l7.682-7.682a4.5 4.5 0 00-6.364-6.364L12 7.636l-1.318-1.318a4.5 4.5 0 00-6.364 0z"></path></svg>';
        }
    }

    initEventListeners() {
        // Add click handlers to favorite buttons
        document.addEventListener('click', (e) => {
            if (e.target.closest('.favorite-btn')) {
                e.preventDefault();
                const btn = e.target.closest('.favorite-btn');
                const hadithKey = btn.dataset.hadithKey;
                const hadithData = btn.dataset.hadithData ?
                    JSON.parse(btn.dataset.hadithData) : null;

                this.toggleFavorite(hadithKey, hadithData);
            }
        });
    }

    showNotification(message, type = 'info') {
        // Create notification element
        const notification = document.createElement('div');
        notification.className = `fixed top-4 right-4 z-50 px-6 py-3 rounded-lg shadow-lg transform transition-all duration-300 translate-x-full`;

        // Set background color based on type
        const colors = {
            success: 'bg-green-500 text-white',
            error: 'bg-red-500 text-white',
            info: 'bg-blue-500 text-white',
            warning: 'bg-yellow-500 text-white'
        };

        notification.className += ' ' + (colors[type] || colors.info);
        notification.textContent = message;

        document.body.appendChild(notification);

        // Animate in
        setTimeout(() => {
            notification.classList.remove('translate-x-full');
        }, 100);

        // Remove after 3 seconds
        setTimeout(() => {
            notification.classList.add('translate-x-full');
            setTimeout(() => {
                document.body.removeChild(notification);
            }, 300);
        }, 3000);
    }

    exportFavorites() {
        const favorites = this.getFavorites();
        const dataStr = JSON.stringify(favorites, null, 2);
        const dataBlob = new Blob([dataStr], { type: 'application/json' });

        const link = document.createElement('a');
        link.href = URL.createObjectURL(dataBlob);
        link.download = 'hadith-favorites-' + new Date().toISOString().split('T')[0] + '.json';
        link.click();

        this.showNotification('Favorit berhasil diekspor', 'success');
    }

    importFavorites(file) {
        const reader = new FileReader();
        reader.onload = (e) => {
            try {
                const imported = JSON.parse(e.target.result);
                if (Array.isArray(imported)) {
                    this.favorites = [...new Set([...this.favorites, ...imported])];
                    this.saveFavorites();
                    this.showNotification('Favorit berhasil diimpor', 'success');
                } else {
                    throw new Error('Invalid format');
                }
            } catch (error) {
                this.showNotification('Gagal mengimpor favorit', 'error');
            }
        };
        reader.readAsText(file);
    }
}

// Initialize favorites manager
let favoritesManager;

document.addEventListener('DOMContentLoaded', function () {
    favoritesManager = new FavoritesManager();

    // Initialize other existing functionality
    // Apply saved theme
    applyTheme();

    // Add search enter key handler
    const searchInput = document.getElementById('searchInput');
    if (searchInput) {
        searchInput.addEventListener('keypress', function (e) {
            if (e.key === 'Enter') {
                performSearch();
            }
        });

        // Auto-save search query
        searchInput.addEventListener('input', function () {
            saveSearchPreference('lastQuery', this.value);
        });

        // Restore last search query
        const lastQuery = getSearchPreference('lastQuery');
        if (lastQuery && !searchInput.value) {
            searchInput.placeholder = `Cari: "${lastQuery}"`;
        }
    }

    const searchInputDkstp = document.getElementById('searchInputDkstp');
    if (searchInputDkstp) {
        searchInputDkstp.addEventListener('keypress', function (e) {
            if (e.key === 'Enter') {
                performSearchDekstop();
            }
        });

        // Auto-save search query
        searchInputDkstp.addEventListener('input', function () {
            saveSearchPreference('lastQuery', this.value);
        });

        // Restore last search query
        const lastQuery = getSearchPreference('lastQuery');
        if (lastQuery && !searchInputDkstp.value) {
            searchInputDkstp.placeholder = `Cari: "${lastQuery}"`;
        }
    }

    // Add smooth scroll for anchor links
    document.querySelectorAll('a[href^="#"]').forEach(anchor => {
        anchor.addEventListener('click', function (e) {
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

// Export for use in templates
window.FavoritesManager = FavoritesManager;
window.favoritesManager = favoritesManager;
