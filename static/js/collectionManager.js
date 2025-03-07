function collectionManager() {
    return {
      newCollectionName: '',
      selectedCollection: '',
      selectedListCollection: '',
      selectedSearchCollection: '',
      searchQuery: '',
      maxResults: 5,
      collections: [],
      entries: [],
      searchResults: [],
      searchError: '',
      searchTimestamp: '',
      fileName: '',
      darkMode: false,
      loading: {
        collections: false,
        create: false,
        upload: false,
        entries: false,
        delete: false,
        search: false,
        reset: false
      },
      
      toggleDarkMode() {
        this.darkMode = !this.darkMode;
        if (this.darkMode) {
          document.documentElement.classList.add('dark');
        } else {
          document.documentElement.classList.remove('dark');
        }
      },

      fetchCollections() {
        this.loading.collections = true;
        fetch('/api/collections')
          .then(response => {
            if (!response.ok) throw new Error('Failed to fetch collections');
            return response.json();
          })
          .then(data => {
            this.collections = data;
          })
          .catch(error => {
            console.error('Error fetching collections:', error);
            this.showToast('error', 'Failed to fetch collections');
          })
          .finally(() => {
            this.loading.collections = false;
          });
      },

      createCollection() {
        if (!this.newCollectionName) return this.showToast('warning', 'Please enter a collection name');
        
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

      uploadFile() {
        if (!this.selectedCollection) return this.showToast('warning', 'Please select a collection');
        const fileInput = document.getElementById('fileUpload');
        if (!fileInput.files.length) return this.showToast('warning', 'Please select a file');

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
        if (!this.selectedListCollection) return this.showToast('warning', 'Please select a collection');
        
        this.loading.delete = entry;
        fetch(`/api/collections/${this.selectedListCollection}/entry/delete`, {
          method: 'DELETE',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ entry: entry })
        })
          .then(response => {
            if (!response.ok) throw new Error('Deletion failed');
            this.showToast('success', 'Entry deleted successfully');
            this.listEntries(); // Refresh the entries list
          })
          .catch(error => {
            console.error('Error deleting entry:', error);
            this.showToast('error', 'Failed to delete entry');
          })
          .finally(() => {
            this.loading.delete = false;
          });
      },

      // New function to confirm before resetting a collection
      confirmResetCollection(collectionName) {
        if (!collectionName) return this.showToast('warning', 'Please select a collection');
        
        Swal.fire({
          title: 'Reset Collection',
          text: `Are you sure you want to reset the "${collectionName}" collection? This will remove all entries and cannot be undone.`,
          icon: 'warning',
          showCancelButton: true,
          confirmButtonColor: '#d97706', // orange-600
          cancelButtonColor: '#6B7280', // gray-500
          confirmButtonText: 'Yes, reset it!',
          cancelButtonText: 'Cancel'
        }).then((result) => {
          if (result.isConfirmed) {
            this.resetCollection(collectionName);
          }
        });
      },
      
      // New function to reset a collection via API
      resetCollection(collectionName) {
        this.loading.reset = collectionName;
        
        fetch(`/api/collections/${collectionName}/reset`, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' }
        })
          .then(response => {
            if (!response.ok) throw new Error('Reset failed');
            this.showToast('success', `Collection "${collectionName}" has been reset successfully`);
            
            // If the reset collection is the currently selected one, refresh its entries
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
            this.fetchCollections();
          });
      },

      searchCollection() {
        // Clear previous errors and results
        this.searchError = '';
        this.searchResults = [];
        
        if (!this.selectedSearchCollection || !this.searchQuery) {
          this.searchError = 'Please select a collection and enter a query';
          return;
        }
        
        const maxResultsVal = parseInt(this.maxResults) || 5;
        this.loading.search = true;
        
        // Set search timestamp
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
                // Try to parse as JSON to get error message
                try {
                  const data = JSON.parse(text);
                  throw new Error(data.error || data.message || 'Search failed with status: ' + response.status);
                } catch (e) {
                  // If parsing fails, use the raw text or status
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
            // Show detailed error in the UI
            this.searchError = error.message || 'An unknown error occurred during search';
            // Also show in toast
            this.showToast('error', 'Search failed: ' + this.searchError);
          })
          .finally(() => {
            this.loading.search = false;
          });
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

      init() {
        // Check for dark mode preference
        this.darkMode = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches;
        if (this.darkMode) {
          document.documentElement.classList.add('dark');
        }
        
        this.fetchCollections();
      }
    };
  }