// Main Router Component
function appRouter() {
  return {
    currentPage: 'search',
    darkMode: false,
    mobileMenuOpen: false,
    collections: [],
    
    navigate(page) {
      this.currentPage = page;
      // Update URL hash for bookmarkable pages
      if (window.location.hash !== '#' + page) {
        window.location.hash = '#' + page;
      }
    },
    
    toggleDarkMode() {
      this.darkMode = !this.darkMode;
      if (this.darkMode) {
        document.documentElement.classList.add('dark');
        localStorage.setItem('darkMode', 'true');
      } else {
        document.documentElement.classList.remove('dark');
        localStorage.setItem('darkMode', 'false');
      }
    },
    
    showToast(type, message) {
      const iconMap = {
        success: 'success',
        error: 'error',
        warning: 'warning',
        info: 'info'
      };
      
      Swal.fire({
        toast: true,
        position: 'top-end',
        icon: iconMap[type] || 'info',
        title: message,
        showConfirmButton: false,
        timer: 3000,
        timerProgressBar: true
      });
    },
    
    fetchCollections() {
      return fetch('/api/collections')
        .then(response => {
          if (!response.ok) throw new Error('Failed to fetch collections');
          return response.json();
        })
        .then(data => {
          if (Array.isArray(data)) {
            this.collections = data;
            return data;
          } else {
            this.collections = [];
            console.error('collections data:', data);
            return [];
          }
        })
        .catch(error => {
          console.error('Error fetching collections:', error);
          this.showToast('error', 'Failed to fetch collections');
          return [];
        });
    },
    
    init() {
      // Check for dark mode preference
      const savedDarkMode = localStorage.getItem('darkMode');
      if (savedDarkMode === 'true' || (!savedDarkMode && window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches)) {
        this.darkMode = true;
        document.documentElement.classList.add('dark');
      }
      
      // Initialize collections
      this.fetchCollections();
      
      // Handle hash-based routing
      const hash = window.location.hash.slice(1);
      if (hash && ['search', 'collections', 'upload', 'sources', 'entries'].includes(hash)) {
        this.currentPage = hash;
      } else {
        this.currentPage = 'search';
        window.location.hash = '#search';
      }
      
      // Listen for hash changes
      window.addEventListener('hashchange', () => {
        const newHash = window.location.hash.slice(1);
        if (['search', 'collections', 'upload', 'sources', 'entries'].includes(newHash)) {
          this.currentPage = newHash;
        }
      });
    }
  };
}

// Helper function to get router instance
function getRouter() {
  const routerElement = document.querySelector('[x-data="appRouter()"]');
  return routerElement ? Alpine.$data(routerElement) : null;
}

// Search Page Component
function searchPage() {
  return {
    selectedSearchCollection: '',
    searchQuery: '',
    maxResults: 5,
    searchResults: [],
    searchError: '',
    searchTimestamp: '',
    loading: {
      search: false
    },
    
    get collections() {
      const router = getRouter();
      return router ? router.collections : [];
    },
    
    searchCollection() {
      this.searchError = '';
      this.searchResults = [];
      
      if (!this.selectedSearchCollection || !this.searchQuery) {
        this.searchError = 'Please select a collection and enter a query';
        return;
      }
      
      const maxResultsVal = parseInt(this.maxResults) || 5;
      this.loading.search = true;
      
      const now = new Date();
      this.searchTimestamp = now.toISOString().replace('T', ' ').substring(0, 19);
      
      fetch(`/api/collections/${this.selectedSearchCollection}/search`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ 
          query: this.searchQuery, 
          max_results: maxResultsVal 
        })
      })
        .then(response => {
          if (!response.ok) {
            return response.text().then(text => {
              try {
                const data = JSON.parse(text);
                throw new Error(data.error || data.message || 'Search failed with status: ' + response.status);
              } catch (e) {
                throw new Error(text || 'Search failed with status: ' + response.status);
              }
            });
          }
          return response.json();
        })
        .then(data => {
          if (data.length === 0) {
            this.searchResults = ['No results found for query: "' + this.searchQuery + '"'];
            return;
          }
          this.searchResults = data.map(item => JSON.stringify(item, null, 2));
        })
        .catch(error => {
          console.error('Error searching collection:', error);
          this.searchError = error.message || 'An unknown error occurred during search';
          this.showToast('error', 'Search failed: ' + this.searchError);
        })
        .finally(() => {
          this.loading.search = false;
        });
    },
    
    showToast(type, message) {
      const router = getRouter();
      if (router) router.showToast(type, message);
    }
  };
}

// Collections Page Component
function collectionsPage() {
  return {
    newCollectionName: '',
    loading: {
      create: false,
      collections: false,
      reset: false
    },
    
    get collections() {
      const router = getRouter();
      return router ? router.collections : [];
    },
    
    createCollection() {
      if (!this.newCollectionName) {
        this.showToast('warning', 'Please enter a collection name');
        return;
      }
      
      this.loading.create = true;
      fetch('/api/collections', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: this.newCollectionName })
      })
        .then(response => {
          if (!response.ok) throw new Error('Failed to create collection');
          return response.json();
        })
        .then(() => {
          this.showToast('success', `Collection "${this.newCollectionName}" created successfully`);
          this.newCollectionName = '';
          this.fetchCollections();
        })
        .catch(error => {
          console.error('Error creating collection:', error);
          this.showToast('error', 'Failed to create collection');
        })
        .finally(() => {
          this.loading.create = false;
        });
    },
    
    fetchCollections() {
      const router = getRouter();
      if (router) {
        this.loading.collections = true;
        router.fetchCollections().finally(() => {
          this.loading.collections = false;
        });
      }
    },
    
    confirmResetCollection(collectionName) {
      if (!collectionName) {
        this.showToast('warning', 'Please select a collection');
        return;
      }
      
      Swal.fire({
        title: 'Reset Collection',
        text: `Are you sure you want to reset the "${collectionName}" collection? This will remove all entries and cannot be undone.`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonColor: '#d97706',
        cancelButtonColor: '#6B7280',
        confirmButtonText: 'Yes, reset it!',
        cancelButtonText: 'Cancel'
      }).then((result) => {
        if (result.isConfirmed) {
          this.resetCollection(collectionName);
        }
      });
    },
    
    resetCollection(collectionName) {
      this.loading.reset = collectionName;
      
      fetch(`/api/collections/${collectionName}/reset`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' }
      })
        .then(response => {
          if (!response.ok) throw new Error('Reset failed');
          this.showToast('success', `Collection "${collectionName}" has been reset successfully`);
          this.fetchCollections();
        })
        .catch(error => {
          console.error('Error resetting collection:', error);
          this.showToast('error', `Failed to reset collection: ${error.message}`);
        })
        .finally(() => {
          this.loading.reset = false;
        });
    },
    
    showToast(type, message) {
      const router = getRouter();
      if (router) router.showToast(type, message);
    },
    
    init() {
      this.fetchCollections();
    }
  };
}

// Upload Page Component
function uploadPage() {
  return {
    selectedCollection: '',
    fileName: '',
    loading: {
      upload: false
    },
    
    get collections() {
      const router = getRouter();
      return router ? router.collections : [];
    },
    
    uploadFile() {
      if (!this.selectedCollection) {
        this.showToast('warning', 'Please select a collection');
        return;
      }
      const fileInput = document.getElementById('fileUpload');
      if (!fileInput.files.length) {
        this.showToast('warning', 'Please select a file');
        return;
      }

      const formData = new FormData();
      formData.append('file', fileInput.files[0]);
      
      this.loading.upload = true;
      fetch(`/api/collections/${this.selectedCollection}/upload`, {
        method: 'POST',
        body: formData
      })
        .then(response => {
          if (!response.ok) throw new Error('Upload failed');
          this.showToast('success', 'File uploaded successfully');
          fileInput.value = '';
          this.fileName = '';
        })
        .catch(error => {
          console.error('Error uploading file:', error);
          this.showToast('error', 'Failed to upload file');
        })
        .finally(() => {
          this.loading.upload = false;
        });
    },
    
    showToast(type, message) {
      const router = getRouter();
      if (router) router.showToast(type, message);
    }
  };
}

// Sources Page Component
function sourcesPage() {
  return {
    selectedSourceCollection: '',
    newSourceURL: '',
    newSourceInterval: 60,
    sources: [],
    loading: {
      sources: false,
      addSource: false,
      removeSource: false
    },
    
    get collections() {
      const router = getRouter();
      return router ? router.collections : [];
    },
    
    listSources() {
      if (!this.selectedSourceCollection) return;
      
      this.loading.sources = true;
      this.sources = [];
      
      fetch(`/api/collections/${this.selectedSourceCollection}/sources`)
        .then(response => {
          if (!response.ok) throw new Error('Failed to list sources');
          return response.json();
        })
        .then(data => {
          this.sources = data;
        })
        .catch(error => {
          console.error('Error listing sources:', error);
          this.showToast('error', 'Failed to fetch sources');
        })
        .finally(() => {
          this.loading.sources = false;
        });
    },
    
    addSource() {
      if (!this.selectedSourceCollection) {
        this.showToast('warning', 'Please select a collection');
        return;
      }
      if (!this.newSourceURL) {
        this.showToast('warning', 'Please enter a source URL');
        return;
      }
      
      const interval = parseInt(this.newSourceInterval) || 60;
      if (interval < 1) {
        this.showToast('warning', 'Update interval must be at least 1 minute');
        return;
      }
      
      this.loading.addSource = true;
      fetch(`/api/collections/${this.selectedSourceCollection}/sources`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ 
          url: this.newSourceURL,
          update_interval: interval
        })
      })
        .then(response => {
          if (!response.ok) throw new Error('Failed to add source');
          return response.json();
        })
        .then(() => {
          this.showToast('success', 'Source added successfully');
          this.newSourceURL = '';
          this.newSourceInterval = 60;
          this.listSources();
        })
        .catch(error => {
          console.error('Error adding source:', error);
          this.showToast('error', 'Failed to add source');
        })
        .finally(() => {
          this.loading.addSource = false;
        });
    },
    
    removeSource(url) {
      if (!this.selectedSourceCollection) {
        this.showToast('warning', 'Please select a collection');
        return;
      }
      
      this.loading.removeSource = url;
      fetch(`/api/collections/${this.selectedSourceCollection}/sources`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ url: url })
      })
        .then(response => {
          if (!response.ok) throw new Error('Failed to remove source');
          return response.json();
        })
        .then(() => {
          this.showToast('success', 'Source removed successfully');
          this.listSources();
        })
        .catch(error => {
          console.error('Error removing source:', error);
          this.showToast('error', 'Failed to remove source');
        })
        .finally(() => {
          this.loading.removeSource = false;
        });
    },
    
    showToast(type, message) {
      const router = getRouter();
      if (router) router.showToast(type, message);
    }
  };
}

// Entries Page Component
function entriesPage() {
  return {
    selectedListCollection: '',
    entries: [],
    loading: {
      entries: false,
      delete: false,
      reset: false
    },
    
    get collections() {
      const router = getRouter();
      return router ? router.collections : [];
    },
    
    listEntries() {
      if (!this.selectedListCollection) return;
      
      this.loading.entries = true;
      this.entries = [];
      fetch(`/api/collections/${this.selectedListCollection}/entries`)
        .then(response => {
          if (!response.ok) throw new Error('Failed to list entries');
          return response.json();
        })
        .then(data => {
          this.entries = data;
        })
        .catch(error => {
          console.error('Error listing entries:', error);
          this.showToast('error', 'Failed to fetch entries');
        })
        .finally(() => {
          this.loading.entries = false;
        });
    },
    
    deleteEntry(entry) {
      if (!this.selectedListCollection) {
        this.showToast('warning', 'Please select a collection');
        return;
      }
      
      this.loading.delete = entry;
      fetch(`/api/collections/${this.selectedListCollection}/entry/delete`, {
        method: 'DELETE',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ entry: entry })
      })
        .then(response => {
          if (!response.ok) throw new Error('Deletion failed');
          this.showToast('success', 'Entry deleted successfully');
          this.listEntries();
        })
        .catch(error => {
          console.error('Error deleting entry:', error);
          this.showToast('error', 'Failed to delete entry');
        })
        .finally(() => {
          this.loading.delete = false;
        });
    },
    
    confirmResetCollection(collectionName) {
      if (!collectionName) {
        this.showToast('warning', 'Please select a collection');
        return;
      }
      
      Swal.fire({
        title: 'Reset Collection',
        text: `Are you sure you want to reset the "${collectionName}" collection? This will remove all entries and cannot be undone.`,
        icon: 'warning',
        showCancelButton: true,
        confirmButtonColor: '#d97706',
        cancelButtonColor: '#6B7280',
        confirmButtonText: 'Yes, reset it!',
        cancelButtonText: 'Cancel'
      }).then((result) => {
        if (result.isConfirmed) {
          this.resetCollection(collectionName);
        }
      });
    },
    
    resetCollection(collectionName) {
      this.loading.reset = collectionName;
      
      fetch(`/api/collections/${collectionName}/reset`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' }
      })
        .then(response => {
          if (!response.ok) throw new Error('Reset failed');
          this.showToast('success', `Collection "${collectionName}" has been reset successfully`);
          if (collectionName === this.selectedListCollection) {
            this.listEntries();
          }
        })
        .catch(error => {
          console.error('Error resetting collection:', error);
          this.showToast('error', `Failed to reset collection: ${error.message}`);
        })
        .finally(() => {
          this.loading.reset = false;
        });
    },
    
    showToast(type, message) {
      const router = getRouter();
      if (router) router.showToast(type, message);
    }
  };
}
