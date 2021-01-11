const functions = require('firebase-functions');
const admin = require('firebase-admin');
admin.initializeApp();

const db = admin.firestore();

exports.redir = functions.https.onRequest((request, response) => {
    let slug = request.url.split("/", 2)[1].split("?", 2)[0].toLocaleLowerCase();
    let query = "";
    {
        let q = request.url.split("?");
        q.shift();
        query = q.join("?");
    }
    let hostname = request.hostname;
    return db.collection(hostname).doc(slug).get().then(documentSnapshot => {
        if (documentSnapshot.exists) {
            let data = documentSnapshot.data();
            documentSnapshot.ref.update({
                clickCount: admin.firestore.FieldValue.increment(1),
                clickLast: admin.firestore.FieldValue.serverTimestamp()
            });
            let destination = data.destination;
            if (data.passQueryString && query !== "") {
                destination = destination + (RegExp("\\?").test(destination) ? "&" : "?") + query;
            }
            response.redirect(destination);
        } else {
            if (slug === "404") {
                response.redirect("/404.html"); // the 404 slug doesn't exist, so fail us to a static 404 page
            } else {
                response.redirect("/404"); // use the 404 slug to send us somewhere
            }
        }
        return;
    });
});